package extraction

import (
	"sort"
	"strings"
)

func splitKey(key string) []string { return strings.SplitN(key, "|", 3) }

// Aggregator merges several scan results into archive-wide statistics. It backs
// both the multi-seed closure report and the full-archive coverage pass.
type Aggregator struct {
	Nodes            int
	TextBytes        int
	States           map[RefState]int
	ByEdge           map[string]int
	Unmodeled        map[string]int
	UnmodeledExample map[string]string
	// perEdge tallies every state a declared edge produced, so "this edge is
	// declared but almost never resolves" is a number rather than a guess.
	perEdge map[string]map[RefState]int
	// perEdgeSample keeps one value per (edge, state): a rate alone says an edge
	// under-resolves, the value says why. Kept as the lexicographically smallest
	// so parallel scans report the same sample twice in a row.
	perEdgeSample map[string]string
	// Unknown 混着两种成因完全相反的判决：同名多个目标是"不许猜"（要靠 roots
	// 或改声明），动态模板是"只有客户端知道"（声明没错，天生判不了）。把它们塞进
	// 同一列会诱导错误结论——本轮就差点把资产边 12 万条模板值读成同名冲突。
	perEdgeAmbiguous map[string]int
	perEdgeDynamic   map[string]int
}

// NewAggregator returns an empty accumulator.
func NewAggregator() *Aggregator {
	return &Aggregator{States: map[RefState]int{}, ByEdge: map[string]int{},
		Unmodeled: map[string]int{}, UnmodeledExample: map[string]string{},
		perEdge: map[string]map[RefState]int{}, perEdgeSample: map[string]string{},
		perEdgeAmbiguous: map[string]int{}, perEdgeDynamic: map[string]int{}}
}

// tallyUnknown splits one unknown reference by the reason the scanner gave it.
// Anything without a recognised reason is deliberately not counted, so the two
// split columns can never add up to more than the Unknown total.
func (a *Aggregator) tallyUnknown(edgeID, note string) {
	if edgeID == "" {
		return
	}
	switch {
	case strings.HasPrefix(note, "同名条目"):
		if a.perEdgeAmbiguous == nil {
			a.perEdgeAmbiguous = map[string]int{}
		}
		a.perEdgeAmbiguous[edgeID]++
	case strings.HasPrefix(note, "动态路径"):
		if a.perEdgeDynamic == nil {
			a.perEdgeDynamic = map[string]int{}
		}
		a.perEdgeDynamic[edgeID]++
	}
}

func (a *Aggregator) tally(edgeID string, state RefState) {
	if a.perEdge == nil {
		a.perEdge = map[string]map[RefState]int{}
	}
	states := a.perEdge[edgeID]
	if states == nil {
		states = map[RefState]int{}
		a.perEdge[edgeID] = states
	}
	states[state]++
}

func (a *Aggregator) sample(edgeID string, state RefState, value string) {
	if a.perEdgeSample == nil || value == "" {
		return
	}
	key := edgeID + "|" + string(state)
	if existing, ok := a.perEdgeSample[key]; !ok || value < existing {
		a.perEdgeSample[key] = value
	}
}

// EdgeStat is one declared edge's measured behaviour over the scanned set.
type EdgeStat struct {
	EdgeID        string
	Values        int
	Resolved      int
	Missing       int
	Unknown       int
	Ambiguous     int
	Dynamic       int
	MissingSample string
	UnknownSample string
}

// Rate is the share of this edge's values that landed on a real entry.
func (s EdgeStat) Rate() float64 {
	if s.Values == 0 {
		return 0
	}
	return float64(s.Resolved) / float64(s.Values)
}

// EdgeStats lists the per-edge tallies. Ordering is worst rate first at equal
// volume: a heavily-used edge that rarely resolves is the mis-declaration worth
// finding, which is exactly what measuring a batch of new edges has to answer.
func (a *Aggregator) EdgeStats() []EdgeStat {
	out := make([]EdgeStat, 0, len(a.perEdge))
	for edgeID, states := range a.perEdge {
		row := EdgeStat{EdgeID: edgeID, Resolved: states[StateResolved],
			Missing: states[StateMissing], Unknown: states[StateUnknown],
			Ambiguous:     a.perEdgeAmbiguous[edgeID],
			Dynamic:       a.perEdgeDynamic[edgeID],
			MissingSample: a.perEdgeSample[edgeID+"|"+string(StateMissing)],
			UnknownSample: a.perEdgeSample[edgeID+"|"+string(StateUnknown)]}
		row.Values = row.Resolved + row.Missing + row.Unknown
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].Rate(), out[j].Rate()
		if left != right {
			return left < right
		}
		if out[i].Values != out[j].Values {
			return out[i].Values > out[j].Values
		}
		return out[i].EdgeID < out[j].EdgeID
	})
	return out
}

// Add folds one closure into the totals.
func (a *Aggregator) Add(c *Closure) {
	if c == nil {
		return
	}
	a.Nodes += len(c.Nodes)
	a.TextBytes += c.TextBytes
	for state, count := range c.States {
		a.States[state] += count
	}
	for edge, count := range c.ByEdge {
		a.ByEdge[edge] += count
		if a.perEdge[edge] == nil {
			a.perEdge[edge] = map[RefState]int{}
		}
		a.perEdge[edge][StateResolved] += count
	}
	for _, ref := range c.Unresolved {
		if ref.EdgeID != "" {
			a.tally(ref.EdgeID, ref.State)
			a.sample(ref.EdgeID, ref.State, clip(ref.Value))
			a.tallyUnknown(ref.EdgeID, ref.Note)
		}
	}
	for key, count := range c.Unmodeled {
		a.Unmodeled[key] += count
	}
	for key, example := range c.UnmodeledExample {
		if existing, ok := a.UnmodeledExample[key]; !ok || example < existing {
			a.UnmodeledExample[key] = example
		}
	}
}

// AddScan folds a single entry scan into the totals.
func (a *Aggregator) AddScan(result ScanResult) {
	a.Nodes++
	a.TextBytes += result.TextBytes
	for _, ref := range result.Refs {
		a.States[ref.State]++
		if ref.State == StateResolved {
			a.ByEdge[ref.EdgeID]++
		}
		if ref.EdgeID != "" {
			a.tally(ref.EdgeID, ref.State)
			a.sample(ref.EdgeID, ref.State, clip(ref.Value))
			a.tallyUnknown(ref.EdgeID, ref.Note)
		}
	}
	for _, item := range result.Unmodeled {
		key := UnmodeledKey(item.Extension, item.Section, item.Shape)
		a.Unmodeled[key]++
		// 样本取字典序最小者而非首次出现者：并行扫描的完成顺序不稳定，
		// "first wins" 会让同一归档两次运行给出不同样例。
		if existing, ok := a.UnmodeledExample[key]; !ok || item.Value < existing {
			a.UnmodeledExample[key] = item.Value
		}
	}
}

// Rows returns the unmodeled queue ordered by frequency, with high-confidence
// path-shaped gaps ahead of numeric ones at equal counts.
func (a *Aggregator) Rows() []UnmodeledRow {
	rows := make([]UnmodeledRow, 0, len(a.Unmodeled))
	for key, count := range a.Unmodeled {
		parts := splitKey(key)
		if len(parts) != 3 {
			continue
		}
		rows = append(rows, UnmodeledRow{Key: key, Extension: parts[0], Section: parts[1],
			Shape: parts[2], Count: count, Example: a.UnmodeledExample[key]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].Shape != rows[j].Shape {
			return rows[i].Shape == "path"
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}

// Coverage reports the share of reference candidates the catalog declared.
func (a *Aggregator) Coverage() float64 {
	declared := a.States[StateResolved] + a.States[StateMissing] + a.States[StateUnknown]
	unmodeled := 0
	for _, count := range a.Unmodeled {
		unmodeled += count
	}
	total := declared + unmodeled
	if total == 0 {
		return 1
	}
	return float64(declared) / float64(total)
}

// PathGapCount totals only high-confidence path-shaped gaps, which is the
// number worth acting on.
func (a *Aggregator) PathGapCount() int {
	total := 0
	for key, count := range a.Unmodeled {
		if len(splitKey(key)) == 3 && splitKey(key)[2] == "path" {
			total += count
		}
	}
	return total
}

// Closure re-exports the accumulated totals as a Closure so callers can reuse
// the same reporting helpers.
func (a *Aggregator) Closure() *Closure {
	return &Closure{States: a.States, ByEdge: a.ByEdge, Unmodeled: a.Unmodeled,
		UnmodeledExample: a.UnmodeledExample, TextBytes: a.TextBytes}
}
