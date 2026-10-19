package extraction

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// ErrCancelled stops a closure when the caller signals cancellation.
var ErrCancelled = errors.New("extraction: 已取消")

// Options bounds an on-demand closure.
type Options struct {
	// MaxEntries caps visited entries. Zero means no cap.
	MaxEntries int
	// MaxDepth caps hops from the nearest seed. Zero means unbounded, which is
	// the fixed-layer semantics pvf callers may expect instead.
	MaxDepth int
	// CollectUnmodeled turns off the candidate queue when only extraction
	// matters and the catalog is known to be complete.
	CollectUnmodeled bool
	// Progress receives an update per visited entry.
	Progress func(visited, scanned int)
	// Cancel is polled before each entry is scanned.
	Cancel func() bool
}

// Link records one resolved reference inside a closure, keeping the catalog
// edge that produced it so results stay attributable.
type Link struct {
	From int32
	To   int32
	Ref  Ref
}

// Node is one visited entry with the reference that pulled it in.
type Node struct {
	Index int32
	Path  string
	Depth int
	// Via is empty for seeds.
	Via Ref
}

// Closure is the reachable subgraph from a set of seeds.
type Closure struct {
	Nodes []Node
	Edges []Link
	// States counts references per resolution state.
	States map[RefState]int
	// ByEdge counts resolved references per catalog edge id.
	ByEdge map[string]int
	// Unresolved keeps every reference that did not resolve, so reports can name
	// the exact source rather than only a count.
	Unresolved []Ref
	// Unmodeled aggregates the candidate queue by extension, section and shape.
	Unmodeled map[string]int
	// UnmodeledExample keeps one sample value per aggregate key.
	UnmodeledExample map[string]string
	// Scanned counts entries whose text was parsed.
	Scanned   int
	TextBytes int
	// Vocabulary and Consumed aggregate the scanned entries' text tokens: how
	// much of the archive's own vocabulary the catalog claims. Unlike States,
	// this ratio is measured against the data rather than against the scanner's
	// own decisions, so improving the catalog is the only way to raise it.
	Vocabulary int
	Consumed   int
	// Truncated says the walk stopped because Options.MaxEntries was reached, with
	// reachable entries still queued. Without it a capped closure is
	// indistinguishable from a complete one, and a cap is exactly the kind of limit
	// a caller must not be able to hit silently.
	Truncated bool
	// Dropped counts reachable entries the cap left unexpanded.
	Dropped int
	// UnmodeledCollected says the candidate queue was enabled for this walk.
	// Coverage is meaningless without it — the denominator would be the scanner's
	// own decisions only — so reports must be able to tell "fully covered" from
	// "not measured".
	UnmodeledCollected bool
}

// UnmodeledKey is the aggregate key used by Unmodeled maps.
func UnmodeledKey(ext, section, shape string) string {
	return ext + "|" + strings.ToLower(section) + "|" + shape
}

// UnmodeledRow is one aggregate of the candidate queue, ordered by frequency.
type UnmodeledRow struct {
	Key       string
	Extension string
	Section   string
	Shape     string
	Count     int
	Example   string
}

// recordGap folds one candidate into the closure's aggregate queue. The closure
// walk is single-threaded, so keeping the first sample is deterministic here.
func recordGap(c *Closure, gap Unmodeled) {
	key := UnmodeledKey(gap.Extension, gap.Section, gap.Shape)
	c.Unmodeled[key]++
	if _, ok := c.UnmodeledExample[key]; !ok {
		c.UnmodeledExample[key] = gap.Value
	}
}

// UnmodeledRows returns the candidate queue sorted by descending count, then by
// shape confidence so path-shaped gaps surface first.
func (c *Closure) UnmodeledRows() []UnmodeledRow {
	rows := make([]UnmodeledRow, 0, len(c.Unmodeled))
	for key, count := range c.Unmodeled {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		rows = append(rows, UnmodeledRow{Key: key, Extension: parts[0], Section: parts[1],
			Shape: parts[2], Count: count, Example: c.UnmodeledExample[key]})
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

// Coverage reports the share of reference candidates the catalog declared, as
// the measurable quality gate for the edge set.
//
// It is self-referential: both the numerator and the denominator come from the
// scanner's own decisions, so it moves when the scanner's bookkeeping moves and
// cannot be recomputed by an outside tool. Kept for continuity — VocabCoverage
// is the auditable replacement.
func (c *Closure) Coverage() float64 {
	declared := c.States[StateResolved] + c.States[StateMissing] + c.States[StateUnknown]
	unmodeled := 0
	for _, count := range c.Unmodeled {
		unmodeled += count
	}
	total := declared + unmodeled
	if total == 0 {
		return 1
	}
	return float64(declared) / float64(total)
}

// VocabCoverage reports how much of the scanned entries' own text vocabulary
// the catalog claims. The denominator is what the archive contains (text
// tokens), not what the scanner decided, so this ratio can be recomputed by
// anyone who can decompile the same entries.
func (c *Closure) VocabCoverage() float64 {
	if c.Vocabulary == 0 {
		return 1
	}
	return float64(c.Consumed) / float64(c.Vocabulary)
}

// Closure walks the reference graph reachable from the seeds, scanning entries
// on demand. It never mutates the archive.
func (s *Scanner) Closure(seeds []int32, opts Options) (*Closure, error) {
	result := &Closure{States: map[RefState]int{}, ByEdge: map[string]int{},
		Unmodeled: map[string]int{}, UnmodeledExample: map[string]string{},
		UnmodeledCollected: opts.CollectUnmodeled}
	type queued struct {
		index int32
		depth int
		via   Ref
	}
	visited := make(map[int32]bool)
	queue := make([]queued, 0, len(seeds))
	for _, seed := range seeds {
		if seed < 0 || visited[seed] {
			continue
		}
		visited[seed] = true
		queue = append(queue, queued{index: seed})
	}
	head := 0
	for head < len(queue) {
		current := queue[head]
		head++
		if opts.Cancel != nil && opts.Cancel() {
			return result, ErrCancelled
		}
		if opts.MaxEntries > 0 && len(result.Nodes) >= opts.MaxEntries {
			result.Truncated = true
			result.Dropped++
			for ; head < len(queue); head++ {
				result.Dropped++
			}
			break
		}
		if s.archive.FileCount() <= current.index {
			continue
		}
		nodePath := s.archive.FullPath(current.index)
		result.Nodes = append(result.Nodes, Node{Index: current.index, Path: nodePath,
			Depth: current.depth, Via: current.via})
		if opts.MaxDepth > 0 && current.depth >= opts.MaxDepth {
			if opts.Progress != nil {
				opts.Progress(len(result.Nodes), s.Scanned())
			}
			continue
		}
		scan, err := s.ScanFile(current.index)
		if err != nil {
			// Undecodable entries are recorded but do not abort the walk; the
			// archive is data, not a proof of well-formedness.
			result.States[StateMissing]++
			continue
		}
		result.TextBytes += scan.TextBytes
		result.Vocabulary += scan.Vocabulary
		result.Consumed += scan.Consumed
		for _, ref := range scan.Refs {
			result.States[ref.State]++
			if ref.State == StateResolved {
				result.ByEdge[ref.EdgeID]++
			} else {
				result.Unresolved = append(result.Unresolved, ref)
			}
			if !ref.Followable() {
				continue
			}
			result.Edges = append(result.Edges, Link{From: current.index, To: ref.Target, Ref: ref})
			if !visited[ref.Target] {
				visited[ref.Target] = true
				queue = append(queue, queued{index: ref.Target, depth: current.depth + 1, via: ref})
			}
		}
		if opts.CollectUnmodeled {
			for _, candidate := range scan.Unmodeled {
				recordGap(result, candidate)
			}
		}
		if opts.Progress != nil {
			opts.Progress(len(result.Nodes), s.Scanned())
		}
	}
	result.Scanned = s.Scanned()
	return result, nil
}

// PathOf returns the archive path of one visited entry.
func (c *Closure) PathOf(index int32) string {
	for i := range c.Nodes {
		if c.Nodes[i].Index == index {
			return c.Nodes[i].Path
		}
	}
	return ""
}

// Paths returns the sorted archive paths of every visited entry.
func (c *Closure) Paths() []string {
	out := make([]string, 0, len(c.Nodes))
	for _, node := range c.Nodes {
		out = append(out, node.Path)
	}
	sort.Strings(out)
	return out
}

// Extensions counts visited entries per file extension.
func (c *Closure) Extensions() map[string]int {
	out := map[string]int{}
	for _, node := range c.Nodes {
		ext := strings.ToLower(path.Ext(node.Path))
		if ext == "" {
			ext = "(noext)"
		}
		out[ext]++
	}
	return out
}
