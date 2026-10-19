package extraction

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"pvfine/internal/pvf"
)

// ManifestName is the machine readable description file written next to the
// exported tree.
const (
	DefaultSubdir    = "pvf"
	ManifestName     = "manifest.json"
	ListFile         = "(1)提取清单.txt"
	TreeFile         = "(2)依赖树.txt"
	UnresolvedFile   = "(3)未解析与未知引用.txt"
	AutoFile         = "(4)自动补入清单.txt"
	RegistrationFile = "(5)登记建议.txt"
	SemanticVersion  = 1
)

// ExportOptions controls a disk export.
type ExportOptions struct {
	// Dir is the destination root. Entries land under Dir/Subdir.
	Dir string
	// Subdir defaults to "pvf".
	Subdir string
	// WriteManifest additionally emits manifest.json, the tree and the
	// unresolved report.
	WithReports bool
	// Plan narrows and organises the export. Nil exports the whole closure in
	// the original layout, which is the pre-plan behaviour.
	Plan *Plan
	// Form overrides the write form; empty takes it from the plan, which
	// defaults to text.
	Form string
	// LstSuggest additionally looks for entries that are referenced but missing
	// from the registry that should list them. Off by default: a registration
	// changes what the game loads, so it must be asked for.
	LstSuggest bool
	// Scanner supplies the registry tables, the catalog and the ability to
	// re-read the exported set. Required for the registration fragments and for
	// LstSuggest.
	Scanner *Scanner
	// NoFragments turns off the registration fragments (they are on by default).
	NoFragments bool
	// NoAssets turns off the resource/NPK cross-reference sheet (it is on by
	// default when a scanner is supplied): artwork is half of what makes a
	// package usable, so silence about images is the failure mode to avoid.
	NoAssets bool
	// Assets answers "which container file holds this image?". Only the layer
	// that knows the external NPK directory can, so it is injected rather than
	// discovered here. Nil still lists the referenced images and says plainly
	// that no index was available.
	Assets   AssetLocator
	Progress func(done, total int)
	Cancel   func() bool
}

// FragmentRecord tells a reader what a registration fragment contains without
// opening it: which table, how many rows, and how many ids were asked for but
// absent from that table.
type FragmentRecord struct {
	Dest     string `json:"dest"`
	Relation string `json:"relation"`
	ListPath string `json:"listPath"`
	Rows     int    `json:"rows"`
	Missing  int    `json:"missing,omitempty"`
}

// fragmentBody renders one fragment. The body stays pure .lst rows so it can be
// appended to the table as-is; the absent ids are marked with a comment line
// because they are the opposite of a row and must not be mistaken for one.
func fragmentBody(f RegistryFragment) string {
	var sb strings.Builder
	for _, row := range f.Rows {
		sb.WriteString(row)
		sb.WriteByte('\n')
	}
	if len(f.MissingIDs) > 0 {
		sb.WriteString("\n")
		sb.WriteString("# 以下 ID 被本次提取引用，但该表里没有对应行（登记前请人工核对）：\n")
		for _, id := range f.MissingIDs {
			sb.WriteString("# ")
			sb.WriteString(id)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// LstSuggestion is one entry to add to a registry list, ready for review.
type LstSuggestion struct {
	Relation     string `json:"relation"`
	ListPath     string `json:"listPath"`
	EntryPath    string `json:"entryPath"`
	ReferencedBy string `json:"referencedBy"`
	EdgeID       string `json:"edgeId,omitempty"`
}

// ExportedFile is one written entry.
type ExportedFile struct {
	Path  string `json:"path"`
	Size  int    `json:"size"`
	Via   string `json:"via,omitempty"`
	Depth int    `json:"depth"`
	// Dest is the path relative to the export root, which the layout decides.
	Dest string `json:"dest,omitempty"`
	// Category is the classification the selection used, "other" included so an
	// unclaimed entry is visible rather than assumed.
	Category string `json:"category,omitempty"`
	// AutoCompleted marks an entry that is present only to keep the set closed.
	AutoCompleted bool `json:"autoCompleted,omitempty"`
	// Form is "text" for a decompiled script or "raw" for archive bytes. The
	// raw fallback happens only where no text form exists, and is recorded per
	// file so a mixed tree is never a surprise.
	Form string `json:"form"`
}

// ContentForm reports whether an entry can be written as text. Only the two
// script payload types have an encoder on the import side; anything else would
// be silently turned into a script entry by a text round trip, so it stays raw.
func ContentForm(a *pvf.Archive, index int32) string {
	switch a.File(index).DataType {
	case pvf.TypeScript, pvf.TypeUnicode:
		return FormText
	}
	return FormRaw
}

// EffectiveForm reports the form an entry will actually be written in, for a
// requested form. Text only applies when it was requested and the entry has a
// text representation; everything else is raw. The writer and every verifier
// must use this one rule, or a verifier ends up judging raw bytes as text.
func EffectiveForm(a *pvf.Archive, index int32, requested string) string {
	if requested == FormText && ContentForm(a, index) == FormText {
		return FormText
	}
	return FormRaw
}

// Payload returns the bytes to write for an entry in the requested form,
// falling back to raw whenever a text form is unavailable or fails to decode.
func Payload(a *pvf.Archive, index int32, form string) ([]byte, string, error) {
	if EffectiveForm(a, index, form) == FormText {
		text, err := a.Text(index)
		if err == nil {
			return []byte(text), FormText, nil
		}
	}
	// 走到这里就是写原字节：要么请求的就是 raw，要么该条目没有文本形态，
	// 要么反编译失败。三种情况都必须按 raw 记账，不能悄悄标成 text。
	raw, err := a.RawBytes(index)
	if err != nil {
		return nil, "", err
	}
	return raw, FormRaw, nil
}

// SelectionRecord captures the knobs an export ran with, so a later reader can
// tell two exports apart without guessing.
type SelectionRecord struct {
	Categories    []string `json:"categories,omitempty"`
	Mode          string   `json:"mode"`
	Layout        string   `json:"layout"`
	Form          string   `json:"form"`
	AutoCompleted []string `json:"autoCompletedPaths,omitempty"`
	DanglingCount int      `json:"danglingCount,omitempty"`
	DanglingRefs  []string `json:"danglingExamples,omitempty"`
	// LstSuggestions are registry additions the export noticed. They are
	// proposals only; nothing is written to any .lst.
	LstSuggestions []LstSuggestion `json:"lstSuggestions,omitempty"`
	// LstSuggestRequested distinguishes "asked and found nothing" from "never
	// asked", so an empty report is not mistaken for a missing feature.
	LstSuggestRequested bool `json:"lstSuggestRequested,omitempty"`
	// RegistryFragments lists the registration-row fragments written under
	// _registries/, so a reader knows which tables the content touched.
	RegistryFragments []FragmentRecord `json:"registryFragments,omitempty"`
}

// Manifest records what an export contains and, crucially, which catalog was
// in effect, so a later run can tell whether results are comparable.
type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Source        string `json:"source"`
	Format        string `json:"format"`
	ClientVersion string `json:"clientVersion"`
	CatalogEdges  int    `json:"catalogEdges"`
	// Coverage is omitted when the walk did not collect the candidate queue: a
	// ratio computed with a disabled denominator input is not a measurement, and
	// printing "1" for it would read as "perfectly covered".
	Coverage *float64 `json:"coverage,omitempty"`
	// VocabCoverage needs no queue: its denominator is the text tokens the scanned
	// entries actually contain, so it is always reportable.
	VocabCoverage float64          `json:"vocabCoverage"`
	Seeds         []string         `json:"seeds"`
	Counts        map[string]any   `json:"counts"`
	Selection     *SelectionRecord `json:"selection,omitempty"`
	Files         []ExportedFile   `json:"files"`
	// Assets carries the image references and their NPK attribution, so a single
	// manifest can answer "which containers to copy" without re-reading the
	// package. It is omitted past manifestAssetLimit, where counts still stand
	// and the text sheet carries the rows.
	Assets []AssetRecord `json:"assets,omitempty"`
}

// ExportResult reports what landed on disk.
type ExportResult struct {
	Files    int
	Bytes    int64
	Dir      string
	Manifest Manifest
	// TextFiles and RawFiles split the output by write form, so a caller can
	// say "13 raw" instead of leaving it to be discovered.
	TextFiles int
	RawFiles  int
	// FragmentFiles are the registration fragments written under _registries/.
	FragmentFiles []string
	// Suggestions lists registry additions that were requested and found.
	Suggestions []LstSuggestion
	// Assets is the resource half of the package: which client images the
	// exported set references and where they live.
	Assets AssetInventory
	// AssetFile is the written cross-reference sheet, empty when suppressed.
	AssetFile string
}

// Export writes the closure to disk preserving archive-internal paths, using
// raw bytes so nothing is re-encoded on the way out.
func Export(a *pvf.Archive, closure *Closure, opts ExportOptions) (ExportResult, error) {
	subdir := opts.Subdir
	if subdir == "" {
		subdir = DefaultSubdir
	}
	root := opts.Dir
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ExportResult{}, err
	}
	result := ExportResult{Dir: root}
	// assetSheet 只在采集到资源清单时非空，随其余报表一起落进 report/ 目录。
	var assetSheet string
	manifest := Manifest{
		SchemaVersion: SemanticVersion,
		Source:        a.SourcePath(),
		Format:        a.Format(),
		ClientVersion: a.ClientVersion(),
		Counts:        map[string]any{},
		VocabCoverage: closure.VocabCoverage(),
		Files:         make([]ExportedFile, 0, len(closure.Nodes)),
	}
	if closure.UnmodeledCollected {
		coverage := closure.Coverage()
		manifest.Coverage = &coverage
	}
	if opts.Scanner != nil && opts.Scanner.Catalog() != nil {
		manifest.CatalogEdges = opts.Scanner.Catalog().EdgeCount()
	}
	plan := opts.Plan
	writeForm := opts.Form
	if writeForm == "" {
		if plan != nil {
			writeForm = plan.Selection.Form
		}
	}
	if writeForm == "" {
		writeForm = FormText
	}
	indexes := make([]int32, 0, len(closure.Nodes))
	if plan != nil {
		indexes = append(indexes, plan.Exported()...)
	} else {
		for _, node := range closure.Nodes {
			indexes = append(indexes, node.Index)
		}
	}
	byIndex := make(map[int32]Node, len(closure.Nodes))
	for _, node := range closure.Nodes {
		byIndex[node.Index] = node
	}
	if plan != nil {
		record := &SelectionRecord{Mode: "closed", Layout: plan.Selection.Layout,
			Categories: plan.Selection.Categories, Form: writeForm,
			DanglingCount: len(plan.Dangling)}
		if plan.Selection.Strict {
			record.Mode = "strict"
		}
		for i, ref := range plan.Dangling {
			if i >= 20 {
				break
			}
			record.DanglingRefs = append(record.DanglingRefs,
				ref.EdgeID+" -> "+ref.TargetPath+" ("+clip(ref.Value)+")")
		}
		manifest.Selection = record
	}
	for _, index := range indexes {
		if opts.Cancel != nil && opts.Cancel() {
			return result, ErrCancelled
		}
		node := byIndex[index]
		if node.Path == "" {
			node.Path = a.FullPath(index)
		}
		dest := node.Path
		if plan != nil {
			relative, err := plan.DestPath(index)
			if err != nil {
				return result, err
			}
			dest = relative
		}
		target, err := resolveUnder(root, subdir, dest)
		if err != nil {
			return result, err
		}
		payload, form, err := Payload(a, index, writeForm)
		if err != nil {
			return result, fmt.Errorf("读取 %s 失败: %w", node.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return result, err
		}
		if err := writeFileAtomic(target, payload); err != nil {
			return result, fmt.Errorf("写入 %s 失败: %w", node.Path, err)
		}
		entry := ExportedFile{Path: filepath.ToSlash(node.Path), Size: len(payload),
			Via: node.Via.EdgeID, Depth: node.Depth, Dest: filepath.ToSlash(dest), Form: form}
		if plan != nil {
			entry.Category = plan.CategoryOf(index)
			entry.AutoCompleted = plan.AutoCompleted(index)
			if entry.AutoCompleted {
				manifest.Selection.AutoCompleted = append(manifest.Selection.AutoCompleted, entry.Dest)
			}
		}
		manifest.Files = append(manifest.Files, entry)
		result.Files++
		result.Bytes += int64(len(payload))
		if form == FormText {
			result.TextFiles++
		} else {
			result.RawFiles++
		}
		if opts.Progress != nil {
			opts.Progress(result.Files, len(indexes))
		}
	}
	exts := closure.Extensions()
	states := map[string]int{}
	for state, count := range closure.States {
		states[state.String()] = count
	}
	manifest.Counts["byExtension"] = exts
	for state, count := range states {
		manifest.Counts[state] = count
	}
	manifest.Counts["textFiles"] = result.TextFiles
	manifest.Counts["rawFiles"] = result.RawFiles
	if plan != nil {
		byCategory := map[string]any{}
		for _, row := range plan.Categories {
			byCategory[row.ID] = map[string]any{
				"name": row.Name, "files": row.Files, "bytes": row.Bytes,
				"refs": row.Refs, "autoCompleted": row.AutoFiles,
			}
		}
		manifest.Counts["byCategory"] = byCategory
		manifest.Counts["exported"] = len(indexes)
		manifest.Counts["autoCompleted"] = len(plan.External)
		if plan.Selection.Strict {
			manifest.Counts["dangling"] = len(plan.Dangling)
		}
	}
	if opts.LstSuggest {
		suggestions, err := SuggestRegistrations(opts.Scanner.RegistrySet(), closure, indexes)
		if err != nil {
			return result, err
		}
		result.Suggestions = make([]LstSuggestion, 0, len(suggestions))
		for _, item := range suggestions {
			result.Suggestions = append(result.Suggestions, LstSuggestion{
				Relation: item.Relation, ListPath: item.ListPath, EntryPath: item.EntryPath,
				ReferencedBy: item.ReferencedBy, EdgeID: item.EdgeID,
			})
		}
		if manifest.Selection == nil {
			manifest.Selection = &SelectionRecord{Mode: "closed", Layout: LayoutMirror, Form: writeForm}
		}
		manifest.Selection.LstSuggestions = result.Suggestions
		manifest.Selection.LstSuggestRequested = true
		manifest.Counts["lstSuggestions"] = len(result.Suggestions)
	}
	// 登记表片段：把本次涉及到的登记行按表导出，随产物一起走。默认开启——
	// 没有它就只剩一堆 ID，没人知道它们指向什么；有它也只是一小段文本。
	if opts.NoFragments != true {
		fragments := BuildRegistryFragments(opts.Scanner, indexes)
		for _, fragment := range fragments {
			dest := fragment.Dest()
			target, err := resolveUnder(root, subdir, dest)
			if err != nil {
				return result, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return result, err
			}
			if err := writeFileAtomic(target, []byte(fragmentBody(fragment))); err != nil {
				return result, err
			}
			result.FragmentFiles = append(result.FragmentFiles, dest)
			if manifest.Selection == nil {
				manifest.Selection = &SelectionRecord{Mode: "closed", Layout: LayoutMirror, Form: writeForm}
			}
			manifest.Selection.RegistryFragments = append(manifest.Selection.RegistryFragments,
				FragmentRecord{Dest: dest, Relation: fragment.Relation, ListPath: fragment.ListPath,
					Rows: len(fragment.Rows), Missing: len(fragment.MissingIDs)})
		}
		manifest.Counts["registryFragments"] = len(result.FragmentFiles)
	}
	// 资源对照：产物里只有脚本的话，装备在客户端上就是一片空白，所以图片归属
	// 必须和脚本一起交付；NPK 在归档之外，只有上层知道目录，这里接受注入。
	if !opts.NoAssets && opts.Scanner != nil {
		inventory, err := BuildAssetInventory(a, opts.Scanner, indexes, opts.Assets)
		if err != nil {
			return result, err
		}
		result.Assets = inventory
		result.AssetFile = AssetFile
		assetSheet = AssetReport(inventory, result.Files)
		manifest.Counts["assets"] = inventory.Total
		manifest.Counts["assetsMatched"] = inventory.Matched
		manifest.Counts["assetsDynamic"] = inventory.Dynamic
		manifest.Counts["assetsUnmatched"] = inventory.Unmatched
		manifest.Counts["assetsIndexed"] = inventory.Indexed
		if len(inventory.Records) <= manifestAssetLimit {
			manifest.Assets = inventory.Records
		} else {
			manifest.Counts["assetsListTruncated"] = len(inventory.Records) - manifestAssetLimit
		}
	}
	for _, seed := range closure.Nodes {
		if seed.Depth == 0 {
			manifest.Seeds = append(manifest.Seeds, seed.Path)
		}
	}
	result.Manifest = manifest
	if opts.WithReports {
		if err := writeReports(root, closure, manifest, assetSheet); err != nil {
			return result, err
		}
	}
	return result, nil
}

func writeReports(root string, closure *Closure, manifest Manifest, assetSheet string) error {
	dir := filepath.Join(root, "report")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	list := &strings.Builder{}
	tree := &strings.Builder{}
	unresolved := &strings.Builder{}
	auto := &strings.Builder{}
	byIndex := make(map[int32]Node, len(closure.Nodes))
	for _, node := range closure.Nodes {
		byIndex[node.Index] = node
	}
	// 清单与依赖树按导入顺序列出实际落盘的条目；带计划时就是计划的导出集，
	// 否则是整个闭包。
	ordered := make([]Node, 0, len(closure.Nodes))
	if manifest.Selection != nil {
		for _, file := range manifest.Files {
			for _, node := range closure.Nodes {
				if filepath.ToSlash(file.Path) == filepath.ToSlash(node.Path) {
					ordered = append(ordered, node)
					break
				}
			}
		}
	} else {
		ordered = append(ordered, closure.Nodes...)
	}
	for _, node := range ordered {
		fmt.Fprintf(list, "%s\t%s\t%s\n", node.Path, node.Via.EdgeID, node.Via.Value)
		fmt.Fprintf(tree, "%s%s\t[%s]\n", strings.Repeat("    ", node.Depth), node.Path, node.Via.Value)
	}
	for _, file := range manifest.Files {
		if !file.AutoCompleted {
			continue
		}
		fmt.Fprintf(auto, "%s\t← 仅为保持闭包自动补入（类别 %s，来源边 %s）\n",
			file.Dest, file.Category, file.Via)
	}
	for _, ref := range closure.Unresolved {
		if ref.State == StateResolved {
			continue
		}
		fmt.Fprintf(unresolved, "%-9s %-24s %-30s %q  %s\n",
			ref.State, ref.EdgeID, ref.Section, clip(ref.Value), ref.Note)
	}
	if manifest.Selection != nil && manifest.Selection.DanglingCount > 0 {
		fmt.Fprintf(unresolved, "\n悬空引用（strict 模式：指向未导出条目，共 %d 条）：\n", manifest.Selection.DanglingCount)
		for _, line := range manifest.Selection.DanglingRefs {
			fmt.Fprintf(unresolved, "  %s\n", line)
		}
	}
	rows := closure.UnmodeledRows()
	fmt.Fprintf(unresolved, "\n未声明引用候选（%d 类）：\n", len(rows))
	for _, row := range rows {
		fmt.Fprintf(unresolved, "  %8d  %-6s %-8s %-30s 例: %q\n", row.Count, row.Shape, row.Extension, row.Section, row.Example)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	writes := []struct {
		name, body string
	}{
		{ListFile, list.String()},
		{TreeFile, tree.String()},
		{UnresolvedFile, unresolved.String()},
	}
	if manifest.Selection != nil {
		writes = append(writes, struct{ name, body string }{AutoFile, auto.String()})
	}
	if manifest.Selection != nil && manifest.Selection.LstSuggestRequested {
		register := &strings.Builder{}
		if len(manifest.Selection.LstSuggestions) == 0 {
			fmt.Fprintf(register, "本次未发现「被导出集引用、却未登记在对应 registry」的条目。\n")
		} else {
			fmt.Fprintf(register, "以下条目被导出集内的引用指向，却在应当收录它的 registry 里查不到。\n")
			fmt.Fprintf(register, "本文件只是提议：写入 registry 会改变游戏加载行为，请人工核对后再登记。\n\n")
			for _, item := range manifest.Selection.LstSuggestions {
				fmt.Fprintf(register, "%-8s %s\n", item.Relation, item.ListPath)
				fmt.Fprintf(register, "    新增: %s\n", item.EntryPath)
				fmt.Fprintf(register, "    原因: %s 通过 %s 引用\n\n", item.ReferencedBy, item.EdgeID)
			}
		}
		writes = append(writes, struct{ name, body string }{RegistrationFile, register.String()})
	}
	if assetSheet != "" {
		writes = append(writes, struct{ name, body string }{AssetFile, assetSheet})
	}
	for _, item := range writes {
		if err := os.WriteFile(filepath.Join(dir, item.name), []byte(item.body), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(root, ManifestName), append(data, '\n'), 0o644)
}

// Violation is a reference whose target did not land in the exported set.
type Violation struct {
	From string
	To   string
	Ref  Ref
	// Kind separates a traversal bug from a catalog gap. "traversal" means a
	// declared edge was not followed; "missing-edge" means an undeclared but
	// resolvable reference exists inside the closure, i.e. the catalog is incomplete.
	Kind string
}

func (v Violation) String() string {
	edge := v.Ref.EdgeID
	if edge == "" {
		edge = "未声明[" + v.Ref.Section + "]"
	}
	return fmt.Sprintf("%s --%s--> %s (%q)", v.From, edge, v.To, clip(v.Ref.Value))
}

// Verify re-reads every exported entry and asserts two things: the set is
// closed under declared edges (a traversal bug), and no undeclared value inside
// the set resolves to an archive entry outside it (a catalog gap). The second
// check is what makes this falsifiable rather than tautological, because a BFS
// that follows every resolved edge trivially satisfies the first.
func Verify(a *pvf.Archive, scanner *Scanner, closure *Closure) ([]Violation, error) {
	indexes := make([]int32, 0, len(closure.Nodes))
	members := make(map[int32]bool, len(closure.Nodes))
	for _, node := range closure.Nodes {
		indexes = append(indexes, node.Index)
		members[node.Index] = true
	}
	return verifyMembers(a, scanner, indexes, members)
}

// verifyMembers runs both closure checks over an explicit member set, so the
// whole-closure audit and a selective export share one implementation.
func verifyMembers(a *pvf.Archive, scanner *Scanner, indexes []int32, members map[int32]bool) ([]Violation, error) {
	violations := []Violation{}
	for _, index := range indexes {
		nodePath := a.FullPath(index)
		scan, err := scanner.ScanFile(index)
		if err != nil {
			return violations, err
		}
		for _, ref := range scan.Refs {
			if !ref.Followable() {
				continue
			}
			if members[ref.Target] {
				continue
			}
			violations = append(violations, Violation{From: nodePath, To: ref.TargetPath, Ref: ref, Kind: "traversal"})
		}
		// Undeclared candidates that nevertheless resolve to a real entry are a
		// missing edge, and the exported set is already incomplete.
		for _, candidate := range scan.Unmodeled {
			if candidate.Shape != "path" || candidate.Value == "" {
				continue
			}
			target, resolved := scanner.resolveCandidate(nodePath, candidate.Value)
			if !resolved {
				continue
			}
			if members[target] {
				continue
			}
			violations = append(violations, Violation{
				From: nodePath, To: a.FullPath(target),
				Ref:  Ref{Kind: TargetEntry, Value: candidate.Value, Section: candidate.Section, Target: target, State: StateUnmodeled},
				Kind: "missing-edge"})
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Kind != violations[j].Kind {
			return violations[i].Kind < violations[j].Kind
		}
		return violations[i].String() < violations[j].String()
	})
	return violations, nil
}

// resolveCandidate applies the same owner-relative / root fallbacks an edge
// would use, without requiring a catalog entry. It exists so the audit can ask
// "was this value meant to be a reference?".
func (s *Scanner) resolveCandidate(fromPath, value string) (int32, bool) {
	value = strings.TrimSpace(value)
	if value == "" || isDynamicPath(value) {
		return -1, false
	}
	index, _, ok, _ := s.resolveRelative(fromPath, value, nil)
	return index, ok
}

// resolveUnder joins an archive-internal path under base/subdir, refusing to
// escape the destination root.
func resolveUnder(base, subdir, internalPath string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(filepath.ToSlash(internalPath), "\\", "/"))
	if clean == "/" || clean == "/." {
		return "", fmt.Errorf("extraction: 归档内部路径非法: %q", internalPath)
	}
	candidate := filepath.Join(base, subdir, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	relative, err := filepath.Rel(base, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("extraction: 拒绝写出到目标目录之外: %q", internalPath)
	}
	return candidate, nil
}

// writeFileAtomic avoids leaving a half-written tree behind.
func writeFileAtomic(target string, data []byte) error {
	tmp := target + ".pvftmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
