package extraction

import (
	"sort"
	"strings"
)

// WidthSource reports the record width the application itself uses when it
// renders a section. The archive's own rendering rules are the authoritative
// declaration of how tokens group into records, so a catalog edge that disagrees
// with them is suspect: line layout is presentation and can be reloaded at any
// time (see ruleconfig hot reload), while reference semantics cannot.
type WidthSource interface {
	DeclaredWidth(filePath, section string) (int, bool)
}

// WidthFinding is one disagreement between a catalog edge and the declared width.
type WidthFinding struct {
	EdgeID    string
	Extension string
	Section   string
	EdgeWidth int
	Declared  int
	// Kind is "mismatch", "edge-only", "declared-only", "no-claim" or "data-driven".
	// "data-driven" marks a count-prefixed record: its width cannot be stated
	// as a single number, so no rendering rule can ever cross-check it and the
	// declaration has to be listed explicitly wherever the lint runs.
	// "no-claim" is *not* a contradiction: the edge reads tokens that live outside
	// the rows the rendering rule describes (a section can hold a variable-length
	// header plus fixed-width body rows). It still has to be registered, so the
	// exemption stays a conscious act rather than a silent gap.
	Kind string
}

func (w WidthFinding) String() string {
	switch w.Kind {
	case "mismatch":
		return w.EdgeID + " " + w.Extension + " [" + w.Section + "] 边声明宽度=" +
			itoa(w.EdgeWidth) + " 但 rendering 声明=" + itoa(w.Declared)
	case "no-claim":
		return w.EdgeID + " " + w.Extension + " [" + w.Section + "] 未声明宽度，而 rendering 对该 section 的" +
			itoa(w.Declared) + " 列规则管的是另一种行（需登记依据）"
	case "data-driven":
		return w.EdgeID + " " + w.Extension + " [" + w.Section + "] 宽度由数据自描述（countPrefixed），rendering 表达不了"
	case "edge-only":
		return w.EdgeID + " " + w.Extension + " [" + w.Section + "] 声明了宽度 " + itoa(w.EdgeWidth) +
			" 但 rendering 没有该 section 的宽度规则（无法交叉验证）"
	default:
		return w.Extension + " [" + w.Section + "] rendering 声明宽度=" + itoa(w.Declared) +
			" 但边目录未在该 section 取引用（可能是漏边）"
	}
}

// WidthAudit cross-checks the catalog's record widths against the authoritative
// rendering widths. SamplePath supplies one representative archive path per
// extension, because rendering rules may match by full path.
func WidthAudit(catalog *Catalog, src WidthSource, samplePath func(ext string) string) []WidthFinding {
	if src == nil || catalog == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []WidthFinding{}
	doc := catalog.Document()
	for i := range doc.Edges {
		edge := doc.Edges[i]
		for _, ext := range edge.Extensions {
			ext = normalizeExt(ext)
			key := ext + "|" + strings.ToLower(edge.Section)
			seen[key] = true
			sample := samplePath(ext)
			if sample == "" {
				continue
			}
			declared, ok := src.DeclaredWidth(sample, edge.Section)
			width := 0
			if edge.Record != nil {
				width = edge.Record.Width
			}
			dataDriven := edge.Record.countPrefixed() != nil
			switch {
			case dataDriven:
				out = append(out, WidthFinding{EdgeID: edge.ID, Extension: ext, Section: edge.Section,
					Declared: declared, Kind: "data-driven"})
			case ok && width > 1 && width != declared:
				out = append(out, WidthFinding{EdgeID: edge.ID, Extension: ext, Section: edge.Section,
					EdgeWidth: width, Declared: declared, Kind: "mismatch"})
			case ok && width <= 1 && declared > 1:
				out = append(out, WidthFinding{EdgeID: edge.ID, Extension: ext, Section: edge.Section,
					EdgeWidth: width, Declared: declared, Kind: "no-claim"})
			case !ok && width > 1:
				out = append(out, WidthFinding{EdgeID: edge.ID, Extension: ext, Section: edge.Section,
					EdgeWidth: width, Kind: "edge-only"})
			}
		}
	}
	// Rendering rules that declare a width nobody consumes may indicate a
	// missing edge rather than a purely cosmetic rule, so surface them too.
	for _, ext := range sortedKeys(samplePathsOf(samplePath)) {
		sample := samplePath(ext)
		for _, section := range catalog.sectionsForExt(ext) {
			if seen[ext+"|"+strings.ToLower(section)] {
				continue
			}
			if declared, ok := src.DeclaredWidth(sample, section); ok && declared > 1 {
				out = append(out, WidthFinding{Extension: ext, Section: section, Declared: declared, Kind: "declared-only"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].EdgeID+out[i].Section < out[j].EdgeID+out[j].Section
	})
	return out
}

// DeadEdges lists catalog edges that produced nothing in a run. A typo in an
// extension or section name otherwise fails silently: it matches no token and
// contributes no reference, while overall coverage can still look healthy.
func DeadEdges(catalog *Catalog, observed map[string]int) []string {
	if catalog == nil {
		return nil
	}
	out := []string{}
	for _, edge := range catalog.Document().Edges {
		if observed[edge.ID] == 0 {
			out = append(out, edge.ID)
		}
	}
	sort.Strings(out)
	return out
}

func samplePathsOf(samplePath func(string) string) map[string]string {
	out := map[string]string{}
	for _, ext := range knownProbeExtensions {
		if p := samplePath(ext); p != "" {
			out[ext] = p
		}
	}
	return out
}

// knownProbeExtensions bounds the audit sweep; it matches the extensions the
// built-in catalog declares.
var knownProbeExtensions = []string{".dgn", ".map", ".mob", ".obj", ".act", ".als", ".ani", ".til",
	".aic", ".twn", ".stk", ".equ", ".ptl", ".key", ".ai", ".atk", ".skl", ".cre", ".qst", ".etc", ".tbl"}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
