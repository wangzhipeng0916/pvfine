package extraction

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// SuggestionConfidence ranks how much evidence backs a proposed edge.
type SuggestionConfidence string

const (
	// ConfidencePath means the gap holds literal file paths, so a new edge is
	// almost certainly correct; only the target extension set is inferred.
	ConfidencePath SuggestionConfidence = "path"
	// ConfidenceReusedSection means the same section name already carries
	// references for another file type, so the relation can be inherited.
	ConfidenceReusedSection SuggestionConfidence = "reused-section"
	// ConfidenceManual means a numeric gap with no corroborating declaration.
	// Measured on a real dungeon, such queues are dominated by delays, weights
	// and coordinates, so these are reported but never auto-applied.
	ConfidenceManual SuggestionConfidence = "manual"
)

// Suggestion turns one unmodeled gap into either a pasteable edge or an
// explicit "needs a human" entry.
type Suggestion struct {
	Gap        UnmodeledRow
	Confidence SuggestionConfidence
	Reason     string
	Edge       *Edge
}

// SuggestRows derives candidate edges from an in-memory unmodeled queue, which
// is what a closure or a full-archive aggregate produces. Ordering is by
// confidence tier, then by how often the gap was seen.
func SuggestRows(rows []UnmodeledRow, catalog *Catalog, limit int) []Suggestion {
	if limit <= 0 || limit > len(rows) {
		limit = len(rows)
	}
	out := make([]Suggestion, 0, len(rows))
	for _, gap := range rows {
		out = append(out, suggest(gap, catalog))
	}
	sort.Slice(out, func(i, j int) bool {
		if confidenceRank(out[i].Confidence) != confidenceRank(out[j].Confidence) {
			return confidenceRank(out[i].Confidence) < confidenceRank(out[j].Confidence)
		}
		if out[i].Gap.Count != out[j].Gap.Count {
			return out[i].Gap.Count > out[j].Gap.Count
		}
		return out[i].Gap.Key < out[j].Gap.Key
	})
	return out[:minInt(limit, len(out))]
}

func suggest(gap UnmodeledRow, catalog *Catalog) Suggestion {
	suggestion := Suggestion{Gap: gap}
	if strings.TrimSpace(gap.Section) == "" {
		// 无 section 的缺口来自非脚本条目（例如未被扫描分支拦截的表文件），
		// 无法生成合法边声明。
		suggestion.Confidence = ConfidenceManual
		suggestion.Reason = "该值不在任何 section 内，无法生成边声明"
		return suggestion
	}
	switch gap.Shape {
	case "path":
		edge := &Edge{
			ID:         proposeID(gap),
			Extensions: []string{gap.Extension},
			Section:    gap.Section,
			TokenTypes: []int32{6},
			Target:     Target{Kind: TargetEntry},
			Note:       "由 edge-bench 未声明队列建议生成，需人工复核记录宽度与目标扩展名。",
		}
		if ext := strings.ToLower(path.Ext(gap.Example)); plausibleExtension(ext) {
			edge.Target.ExpectExt = []string{ext}
		}
		if sibling := catalog.EdgeForSectionAnywhere(gap.Section); sibling != nil {
			edge.Target.Kind = sibling.Target.Kind
			edge.Target.Relation = sibling.Target.Relation
			edge.Record = sibling.Record
			suggestion.Confidence = ConfidenceReusedSection
			suggestion.Reason = "同名 section 已在 " + strings.Join(sibling.Extensions, "/") +
				" 中被声明为引用，可直接继承其目标定义"
		} else {
			suggestion.Confidence = ConfidencePath
			suggestion.Reason = "该 section 的值是反引号文件路径，形状本身即为证据"
		}
		suggestion.Edge = edge
	default:
		suggestion.Confidence = ConfidenceManual
		suggestion.Reason = "数值型缺口：实测队列里此类几乎全是 delay/weight/坐标，" +
			"只有当同名 section 在别处已声明为 registry 引用时才可安全推断"
		if sibling := catalog.EdgeForSectionAnywhere(gap.Section); sibling != nil &&
			sibling.Target.Kind == TargetRegistry {
			suggestion.Confidence = ConfidenceReusedSection
			suggestion.Reason = "同名 section 已在 " + strings.Join(sibling.Extensions, "/") +
				" 声明为 registry 引用，可继承 relation"
			suggestion.Edge = &Edge{
				ID: proposeID(gap), Extensions: []string{gap.Extension}, Section: gap.Section,
				TokenTypes: []int32{0}, Record: sibling.Record,
				Target: Target{Kind: TargetRegistry, Relation: sibling.Target.Relation,
					NoneValues: sibling.Target.NoneValues},
				Note: "由 edge-bench 未声明队列建议生成（继承同名 section 的既有声明）。",
			}
		}
	}
	return suggestion
}

// MarshalSuggestions renders the auto-applicable subset as a pasteable catalog
// fragment, so a reviewed suggestion can be appended to the edge file directly.
func MarshalSuggestions(items []Suggestion) ([]byte, error) {
	edges := []Edge{}
	for _, item := range items {
		if item.Edge != nil {
			edges = append(edges, *item.Edge)
		}
	}
	if len(edges) == 0 {
		return []byte("{\n  \"edges\": []\n}\n"), nil
	}
	out, err := json.MarshalIndent(struct {
		Version int    `json:"version"`
		Edges   []Edge `json:"edges"`
	}{Version: 1, Edges: edges}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func proposeID(gap UnmodeledRow) string {
	ext := strings.TrimPrefix(gap.Extension, ".")
	clean := make([]rune, 0, len(gap.Section))
	for _, r := range strings.ToLower(gap.Section) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			clean = append(clean, r)
		case r == ' ' || r == '-' || r == '_':
			clean = append(clean, '_')
		}
	}
	if len(clean) == 0 {
		clean = []rune("section")
	}
	return ext + "." + string(clean)
}

func confidenceRank(c SuggestionConfidence) int {
	switch c {
	case ConfidencePath:
		return 0
	case ConfidenceReusedSection:
		return 1
	default:
		return 2
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
