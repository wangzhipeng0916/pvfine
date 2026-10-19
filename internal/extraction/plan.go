package extraction

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"pvfine/internal/pvf"
)

// Layout selects how an exported tree is organised.
const (
	LayoutMirror   = "mirror"
	LayoutCategory = "category"
	LayoutSeed     = "seed"
	// ExternalDir is the top-level directory that holds entries pulled in only
	// to keep the set closed. Criterion 5 requires every auto-completed entry
	// to live here, so a consumer can tell "you asked for this" from "this came
	// along for the ride" without reading the manifest.
	ExternalDir = "_external"
)

// Selection narrows a closure to what the user actually asked for.
type Selection struct {
	// Categories are the selected category ids. Empty means every category.
	Categories []string
	// Paths is an explicit whitelist of archive paths. It is what makes an E13
	// package plan exportable: the plan decides "which entries belong in this
	// MOD", the selection then hands exactly that list to the exporter. When set,
	// a node is selected only if it is listed — not even a seed gets in by
	// default, because "I named these" and "the planner named these" are the same
	// statement here.
	Paths []string
	// Strict exports only the selected set. Without it the set is closed by
	// pulling in whatever the selection references, and those entries are
	// marked autoCompleted instead of silently blended in.
	Strict bool
	// Layout is mirror, category or seed.
	Layout string
	// Form is the write form: FormText (default) writes the decompiled script,
	// which is what the application's source-import text mode reads back;
	// FormRaw keeps the archive bytes for byte-exact needs. Entries with no
	// text representation are always written raw, and the manifest says which
	// is which.
	Form string
}

// Write forms. They mirror the import modes one-for-one, so an extracted tree
// can be edited and imported back with the feature the application already has.
const (
	FormText = "text"
	FormRaw  = "raw"
)

// CategorySummary is the per-category preview row: file counts, bytes,
// references, how much would be auto-completed, and samples.
type CategorySummary struct {
	ID        string
	Name      string
	Files     int
	Bytes     int64
	Refs      int
	AutoFiles int
	Samples   []string
}

// Plan is the answer to "what would this export contain": computed entirely in
// memory, so a preview is cheap enough to run before every export.
type Plan struct {
	Selection  Selection
	Closure    *Closure
	Categories []CategorySummary
	// Main is the selected set, Ancestors keeps it navigable and External keeps
	// it closed. Strict mode leaves External empty and fills Dangling instead.
	Main      []int32
	Ancestors []int32
	External  []int32
	Dangling  []Ref
	// UnlistedPaths are the Selection.Paths the closure does not contain. A caller
	// that asks for an entry that is not there must be told, otherwise a typo in a
	// package plan reads as "the package simply doesn't need it".
	UnlistedPaths []string

	classOf map[int32]string
	autoSet map[int32]bool
	names   map[string]string
	// dirName is the per-category directory used by LayoutCategory. It is built
	// from the declared order and display name, then sanitized: category names
	// come from an editable JSON file, so they must never be able to escape the
	// export root.
	dirName  map[string]string
	parent   map[int32]int32
	seedOf   map[int32]string
	order    []int32
	sizes    map[int32]int
	rootedIn map[int32]bool
}

// Exported reports every entry the plan would write, in a stable order.
func (p *Plan) Exported() []int32 { return p.order }

// ExportedCount reports how many entries would be written.
func (p *Plan) ExportedCount() int { return len(p.order) }

// AutoCompleted reports whether an entry is present only to keep the set closed.
func (p *Plan) AutoCompleted(index int32) bool { return p.autoSet[index] }

// CategoryOf reports the category decided for a node.
func (p *Plan) CategoryOf(index int32) string { return p.classOf[index] }

// DestPath returns the destination-relative path of an exported entry under the
// plan's layout. Auto-completed entries always live under ExternalDir.
func (p *Plan) DestPath(index int32) (string, error) {
	nodePath := p.Closure.PathOf(index)
	clean, err := safeRelative(nodePath)
	if err != nil {
		return "", err
	}
	if p.autoSet[index] {
		if p.Selection.Layout == LayoutCategory {
			return path.Join(ExternalDir, p.categoryDir(p.classOf[index]), clean), nil
		}
		return path.Join(ExternalDir, clean), nil
	}
	switch p.Selection.Layout {
	case LayoutCategory:
		return path.Join(p.categoryDir(p.classOf[index]), clean), nil
	case LayoutSeed:
		return path.Join(seedDirName(p.seedFor(index)), clean), nil
	default:
		return clean, nil
	}
}

// categoryDir returns the directory name of a category, falling back to the id
// so an undeclared bucket still lands somewhere predictable.
func (p *Plan) categoryDir(id string) string {
	if name, ok := p.dirName[id]; ok && name != "" {
		return name
	}
	return categoryDirName(id)
}

// seedFor returns the seed a node was reached from. A node reachable from
// several seeds belongs to the first one in seed order, so LayoutSeed stays
// deterministic.
func (p *Plan) seedFor(index int32) string {
	if p.seedOf == nil {
		p.seedOf = map[int32]string{}
	}
	if seed, ok := p.seedOf[index]; ok {
		return seed
	}
	seen := []int32{}
	for cursor := index; ; {
		if _, ok := p.rootedIn[cursor]; ok {
			// reached a seed
			seed := p.Closure.PathOf(cursor)
			for _, visited := range seen {
				p.seedOf[visited] = seed
			}
			p.seedOf[cursor] = seed
			return seed
		}
		up, ok := p.parent[cursor]
		if !ok {
			for _, visited := range seen {
				p.seedOf[visited] = ""
			}
			p.seedOf[cursor] = ""
			return ""
		}
		seen = append(seen, cursor)
		cursor = up
	}
}

// BuildPlan classifies the closure, applies the selection and completes it.
func BuildPlan(archive *pvf.Archive, catalog *Catalog, closure *Closure, sel Selection) (*Plan, error) {
	if catalog == nil || closure == nil {
		return nil, fmt.Errorf("extraction: 构建计划需要边目录与闭包")
	}
	if sel.Layout == "" {
		sel.Layout = LayoutMirror
	}
	if sel.Form == "" {
		sel.Form = FormText
	}
	switch sel.Layout {
	case LayoutMirror, LayoutCategory, LayoutSeed:
	default:
		return nil, fmt.Errorf("extraction: 未知布局 %q", sel.Layout)
	}
	switch sel.Form {
	case FormText, FormRaw:
	default:
		return nil, fmt.Errorf("extraction: 未知写出形态 %q", sel.Form)
	}
	plan := &Plan{Selection: sel, Closure: closure,
		classOf:  make(map[int32]string, len(closure.Nodes)),
		autoSet:  make(map[int32]bool),
		names:    map[string]string{OtherCategory: "其他"},
		dirName:  map[string]string{OtherCategory: "99-其他"},
		parent:   closureParent(closure),
		rootedIn: make(map[int32]bool),
		sizes:    make(map[int32]int, len(closure.Nodes))}
	for _, cat := range catalog.Categories() {
		name := cat.Name
		if name == "" {
			name = cat.ID
		}
		plan.names[cat.ID] = name
		prefix := ""
		if cat.Order > 0 {
			prefix = fmt.Sprintf("%02d-", cat.Order)
		}
		plan.dirName[cat.ID] = prefix + sanitizeSegment(name)
	}
	for _, node := range closure.Nodes {
		if node.Depth == 0 {
			plan.rootedIn[node.Index] = true
		}
		if archive != nil {
			plan.sizes[node.Index] = int(archive.File(node.Index).DataSize)
		}
	}

	chosen := map[string]bool{}
	for _, id := range sel.Categories {
		if id == "" {
			continue
		}
		if _, ok := catalog.CategoryByID(id); !ok && id != OtherCategory {
			return nil, fmt.Errorf("extraction: 未知类别 %q", id)
		}
		chosen[id] = true
	}
	all := len(chosen) == 0

	byPaths := len(sel.Paths) > 0
	listed := make(map[string]bool, len(sel.Paths))
	for _, entryPath := range sel.Paths {
		entryPath = strings.ToLower(strings.TrimSpace(entryPath))
		if entryPath != "" {
			listed[entryPath] = true
		}
	}
	for _, node := range closure.Nodes {
		class := catalog.ClassifyNode(node)
		plan.classOf[node.Index] = class.ID
		if byPaths {
			key := strings.ToLower(node.Path)
			if listed[key] {
				plan.Main = append(plan.Main, node.Index)
				delete(listed, key)
			}
			continue
		}
		// 种子是用户明确点名的，任何筛选都不该把它们排除在外。
		if node.Depth == 0 || all || chosen[class.ID] {
			plan.Main = append(plan.Main, node.Index)
		}
	}
	for key := range listed {
		plan.UnlistedPaths = append(plan.UnlistedPaths, key)
	}
	sort.Strings(plan.UnlistedPaths)

	inSet := make(map[int32]bool, len(plan.Main)*2)
	for _, index := range plan.Main {
		inSet[index] = true
	}
	// 祖先补链：勾了子项就必须带上到种子的路径，否则目录里会出现"有子无父"。
	for _, index := range plan.Main {
		for cursor := index; ; {
			up, ok := plan.parent[cursor]
			if !ok || inSet[up] {
				break
			}
			inSet[up] = true
			plan.Ancestors = append(plan.Ancestors, up)
			cursor = up
		}
	}
	// 外联：集合内引用到集合外的条目。默认拉进来（保闭包），strict 只记账；
	// 补入本身可能再引用集合外的东西，所以必须迭代到不动点。
	for grew := true; grew; {
		grew = false
		for _, link := range closure.Edges {
			if !inSet[link.From] || inSet[link.To] {
				continue
			}
			if sel.Strict {
				plan.Dangling = append(plan.Dangling, link.Ref)
				continue
			}
			inSet[link.To] = true
			plan.External = append(plan.External, link.To)
			plan.autoSet[link.To] = true
			grew = true
		}
		if sel.Strict {
			break
		}
	}

	sortIndexes(plan.Main)
	sortIndexes(plan.Ancestors)
	sortIndexes(plan.External)
	plan.order = make([]int32, 0, len(inSet))
	for index := range inSet {
		plan.order = append(plan.order, index)
	}
	sortIndexes(plan.order)
	sort.Slice(plan.Dangling, func(i, j int) bool {
		if plan.Dangling[i].EdgeID != plan.Dangling[j].EdgeID {
			return plan.Dangling[i].EdgeID < plan.Dangling[j].EdgeID
		}
		if plan.Dangling[i].Value != plan.Dangling[j].Value {
			return plan.Dangling[i].Value < plan.Dangling[j].Value
		}
		return plan.Dangling[i].TargetPath < plan.Dangling[j].TargetPath
	})
	plan.summarize()
	return plan, nil
}

// summarize fills the per-category preview rows.
func (p *Plan) summarize() {
	type acc struct {
		files int
		bytes int64
		refs  int
		auto  int
		ex    []string
	}
	totals := map[string]*acc{}
	for _, node := range p.Closure.Nodes {
		id := p.classOf[node.Index]
		row, ok := totals[id]
		if !ok {
			row = &acc{}
			totals[id] = row
		}
		row.files++
		row.bytes += int64(p.sizes[node.Index])
		if len(row.ex) < 3 {
			row.ex = append(row.ex, node.Path)
		}
		if p.autoSet[node.Index] {
			row.auto++
		}
	}
	for _, link := range p.Closure.Edges {
		if row, ok := totals[p.classOf[link.To]]; ok {
			row.refs++
		}
	}
	for id, row := range totals {
		name := p.names[id]
		if name == "" {
			name = id
		}
		p.Categories = append(p.Categories, CategorySummary{
			ID: id, Name: name, Files: row.files, Bytes: row.bytes,
			Refs: row.refs, AutoFiles: row.auto, Samples: row.ex,
		})
	}
	sort.SliceStable(p.Categories, func(i, j int) bool {
		if p.Categories[i].Files != p.Categories[j].Files {
			return p.Categories[i].Files > p.Categories[j].Files
		}
		return p.Categories[i].ID < p.Categories[j].ID
	})
}

// closureParent derives the BFS tree parent of every visited node. The first
// link that reaches a node is the one the traversal used.
func closureParent(closure *Closure) map[int32]int32 {
	parent := make(map[int32]int32, len(closure.Nodes))
	for _, link := range closure.Edges {
		if _, seen := parent[link.To]; !seen {
			parent[link.To] = link.From
		}
	}
	return parent
}

func categoryDirName(id string) string {
	switch id {
	case OtherCategory:
		return "99-其他"
	case "":
		return "99-未分类"
	}
	return sanitizeSegment(id)
}

// sanitizeSegment makes an editable label safe to use as one path segment.
// Category names live in a JSON file anyone can edit, so separators and dots
// are stripped rather than trusted — a name must never be able to spell a
// traversal, and ".." is only the most obvious way to try.
func sanitizeSegment(label string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\t", "_", ".", "_")
	out := replacer.Replace(strings.TrimSpace(label))
	out = strings.Trim(out, "_ ")
	if out == "" {
		return "未命名"
	}
	return out
}

func seedDirName(seed string) string {
	if seed == "" {
		return "00-种子"
	}
	base := path.Base(seed)
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" || base == "." || base == "/" {
		return "00-种子"
	}
	return base
}

// safeRelative rejects paths that would escape the destination root.
func safeRelative(internalPath string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(internalPath, "\\", "/"))
	if clean == "/" || clean == "/." {
		return "", fmt.Errorf("extraction: 归档内部路径非法: %q", internalPath)
	}
	return strings.TrimPrefix(clean, "/"), nil
}

func sortIndexes(list []int32) {
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
}

// VerifyPlan re-reads every entry the plan would export and asserts the set is
// closed under declared edges. In strict mode the traversal violations must be
// exactly the recorded dangling references — anything else means the plan and
// the walk disagree.
func VerifyPlan(a *pvf.Archive, scanner *Scanner, plan *Plan) ([]Violation, error) {
	exported := plan.Exported()
	members := make(map[int32]bool, len(exported))
	for _, index := range exported {
		members[index] = true
	}
	violations, err := verifyMembers(a, scanner, exported, members)
	if err != nil {
		return nil, err
	}
	if plan.Selection.Strict {
		traversal := 0
		for _, v := range violations {
			if v.Kind == "traversal" {
				traversal++
			}
		}
		if traversal != len(plan.Dangling) {
			return violations, fmt.Errorf("extraction: strict 模式下游离引用 %d 条，但遍历违反 %d 条，二者必须相等",
				len(plan.Dangling), traversal)
		}
	}
	return violations, nil
}
