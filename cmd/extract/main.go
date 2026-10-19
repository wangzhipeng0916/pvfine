// Command extract is the feasibility harness for the extraction centre: it runs
// a real seed set through catalog -> closure -> on-disk export, then applies
// three falsifiable checks instead of trusting the traversal's own bookkeeping.
//
//	go run ./cmd/extract -out E:/scratch/extract -seed dungeon/act1/grakkarak.dgn
//
// Archive path comes from -pvf or PVF_TESTFILE. It is read-only with respect to
// the archive and writes only under -out.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
	"pvfine/internal/rendering"
)

type seedsFlag []string

func (s *seedsFlag) String() string { return strings.Join(*s, ",") }

func (s *seedsFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type categoriesFlag []string

func (c *categoriesFlag) String() string { return strings.Join(*c, ",") }

func (c *categoriesFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			*c = append(*c, trimmed)
		}
	}
	return nil
}

// widthAdapter exposes the archive's own rendering widths as the cross-check
// oracle for catalog record widths.
type widthAdapter struct {
	engine  *rendering.Engine
	version string
}

func (w widthAdapter) DeclaredWidth(filePath, section string) (int, bool) {
	spec := w.engine.SectionFormat(filePath, section).ForVersion(w.version)
	if spec.TokensPerLine <= 1 {
		return 0, false
	}
	return spec.TokensPerLine, true
}

func main() {
	var seeds seedsFlag
	archivePath := flag.String("pvf", os.Getenv("PVF_TESTFILE"), "Script.pvf 路径，默认取 PVF_TESTFILE")
	out := flag.String("out", "", "导出目录（必填）")
	flag.Var(&seeds, "seed", "种子：归档内条目路径、关系名:ID 或纯 ID，可重复")
	catalogPath := flag.String("catalog", "", "自定义边目录；缺省用内置")
	maxEntries := flag.Int("max", 0, "闭包条目上限，0 为不限")
	layout := flag.String("layout", "mirror", "导出布局：mirror | category | seed")
	strict := flag.Bool("strict", false, "严格子集：只导勾选项，并把悬空引用记进 manifest")
	form := flag.String("form", "text", "写出形态：text = 反编译文本（与源码导入的文本模式对应，默认）| raw = 原字节")
	lstSuggest := flag.Bool("lst-suggest", false, "额外产出 (5)登记建议.txt：被引用但未登记在 registry 里的条目（默认关）")
	noFragments := flag.Bool("no-fragments", false, "不产出 _registries/ 里的登记表片段（默认产出）")
	quiet := flag.Bool("quiet", false, "只输出结论")
	var categories categoriesFlag
	flag.Var(&categories, "category", "只导出该类别，可重复；缺省导出全部类别")
	flag.Parse()

	if *archivePath == "" || *out == "" || len(seeds) == 0 {
		fmt.Fprintln(os.Stderr, "extract: 需要 -pvf(或 PVF_TESTFILE)、-out 与至少一个 -seed")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	catalogData := extraction.EmbeddedEdges
	if *catalogPath != "" {
		data, err := os.ReadFile(*catalogPath)
		if err != nil {
			fail(err)
		}
		catalogData = data
	}
	catalog, err := extraction.LoadCatalog(catalogData)
	if err != nil {
		fail(err)
	}
	archive, err := pvf.Open(*archivePath)
	if err != nil {
		fail(err)
	}
	lists, err := annotations.ParseLists(config.ListsJSON)
	if err != nil {
		fail(err)
	}
	specs := withExtraRelations(lists.Relations)
	renderEngine, err := rendering.Parse(config.RenderingJSON)
	if err != nil {
		fail(err)
	}
	scanner := extraction.NewScanner(archive, catalog, extraction.NewRegistrySet(archive, specs))
	samples := samplePaths(archive)
	sample := func(ext string) string { return samples[ext] }

	fmt.Printf("archive=%s entries=%d clientVersion=%s catalogEdges=%d\n",
		*archivePath, archive.FileCount(), archive.ClientVersion(), catalog.EdgeCount())

	indexes := []int32{}
	for _, seed := range seeds {
		// 与产品路径同一套种子语义（路径 / 关系名:ID / 纯 ID）：预设说明里广告
		// 的写法必须在这条端到端管线上也能用，否则量的就不是同一件事。
		hit, err := extraction.ResolveSeed(archive, scanner.RegistrySet(), seed)
		if err != nil {
			fmt.Printf("!! %v\n", err)
			continue
		}
		indexes = append(indexes, hit.Index)
	}
	if len(indexes) == 0 {
		fail(fmt.Errorf("没有可用种子"))
	}

	// Check 0: end-to-end timing and export.
	// 预热对应产品路径里的"载入归档后"：basename 索引 + 全部 registry 一次性付清，
	// 之后的闭包/预览不应再有一次性尖峰。
	warmStarted := time.Now()
	if err := scanner.Prewarm(context.Background()); err != nil {
		fail(err)
	}
	warmElapsed := time.Since(warmStarted)
	started := time.Now()
	closure, err := scanner.Closure(indexes, extraction.Options{
		MaxEntries: *maxEntries, CollectUnmodeled: true,
	})
	closureElapsed := time.Since(started)
	if err != nil {
		fail(err)
	}
	fmt.Printf("预热=%s\n", warmElapsed.Round(time.Millisecond))

	// 预览：按类别投影闭包并套用勾选，全部在内存里算，不落盘。
	planStarted := time.Now()
	plan, err := extraction.BuildPlan(archive, catalog, closure, extraction.Selection{
		Categories: categories, Strict: *strict, Layout: *layout, Form: *form,
	})
	if err != nil {
		fail(err)
	}
	planElapsed := time.Since(planStarted)
	fmt.Printf("\n[预览] 勾选=%v 模式=%s 布局=%s 耗时=%s\n",
		describeSelection(categories), modeName(*strict), *layout, planElapsed.Round(time.Microsecond))
	fmt.Printf("%-16s %8s %10s %8s %10s  %s\n", "类别", "条目", "字节", "引用", "将补入", "示例")
	for _, row := range plan.Categories {
		sample := ""
		if len(row.Samples) > 0 {
			sample = row.Samples[0]
		}
		fmt.Printf("%-16s %8d %10d %8d %10d  %s\n", row.Name, row.Files, row.Bytes, row.Refs, row.AutoFiles, sample)
	}
	fmt.Printf("主集=%d 祖先补链=%d 自动补入=%d 悬空=%d → 实际落盘=%d\n",
		len(plan.Main), len(plan.Ancestors), len(plan.External), len(plan.Dangling), plan.ExportedCount())
	if row, ok := otherRow(plan); ok {
		fmt.Printf("!! 有 %d 个条目落在「其他」类别：%s\n", row.Files, strings.Join(row.Samples, " "))
	}

	result, err := extraction.Export(archive, closure, extraction.ExportOptions{
		Dir: *out, WithReports: true, Plan: plan,
		LstSuggest: *lstSuggest, Scanner: scanner,
		NoFragments: *noFragments,
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("\n闭包=%d 条目  导出=%d 文件 / %.2f MiB  耗时 闭包=%s 导出=%s\n",
		len(closure.Nodes), result.Files, float64(result.Bytes)/1048576,
		closureElapsed.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	fmt.Printf("引用状态=%v 覆盖率=%.4f 词汇消费率=%.4f（%d/%d 文本 token） 扩展名=%s\n",
		closure.States, closure.Coverage(), closure.VocabCoverage(), closure.Consumed, closure.Vocabulary,
		joinCounts(closure.Extensions()))

	// Check 1: the exported set must be closed under the reference relation.
	violations, err := extraction.Verify(archive, scanner, closure)
	if err != nil {
		fail(err)
	}
	traversal, missingEdge := 0, 0
	for _, v := range violations {
		if v.Kind == "missing-edge" {
			missingEdge++
		} else {
			traversal++
		}
	}
	fmt.Printf("\n[判据1 封闭性] 遍历违反=%d 漏边(未声明但可解析)=%d\n", traversal, missingEdge)
	for i, v := range violations {
		if i >= 15 {
			fmt.Printf("   ... 共 %d\n", len(violations))
			break
		}
		fmt.Println("   ", v.Kind, v)
	}

	// Check 2: catalog widths must agree with the archive's own rendering widths.
	findings := extraction.WidthAudit(catalog, widthAdapter{engine: renderEngine, version: archive.ClientVersion()}, sample)
	fmt.Printf("\n[判据2 宽度来源差分] 发现=%d\n", len(findings))
	for i, f := range findings {
		if i >= 20 {
			fmt.Printf("   ... 共 %d\n", len(findings))
			break
		}
		fmt.Println("   ", f.String())
	}

	// Check 3: the written form must be faithful — raw bytes by comparison,
	// text by UTF-8 validity plus semantic equality with the archive entry.
	rawBad, textBad, textFiles, rawFiles := verifyBytes(archive, closure, plan, *out)
	fmt.Printf("\n[判据3 形态保真] 文本=%d（不一致=%d，含非 UTF-8） 原字节=%d（不一致=%d）\n",
		textFiles, textBad, rawFiles, rawBad)

	// Check 4: the completed set must be closed; strict mode must report exactly
	// as many violations as it admitted dangling references.
	planViolations, err := extraction.VerifyPlan(archive, scanner, plan)
	if err != nil {
		fmt.Printf("\n[判据4 补入后封闭性] 失败：%v\n", err)
	} else {
		planTraversal, planMissingEdge := 0, 0
		for _, v := range planViolations {
			if v.Kind == "missing-edge" {
				planMissingEdge++
			} else {
				planTraversal++
			}
		}
		fmt.Printf("\n[判据4 补入后封闭性] 遍历违反=%d 漏边=%d（strict 应等于悬空数 %d）\n",
			planTraversal, planMissingEdge, len(plan.Dangling))
		for i, v := range planViolations {
			if i >= 10 {
				fmt.Printf("   ... 共 %d\n", len(planViolations))
				break
			}
			fmt.Println("   ", v.Kind, v)
		}
	}

	// Check 5: the manifest alone must be enough to rebuild the exported set,
	// and every auto-completed entry must sit under _external/.
	autoOutside, manifestFiles := verifyManifest(plan, *out, result.Manifest)
	fmt.Printf("\n[判据5 补入可复原] manifest 文件数=%d 计划落盘=%d 自动补入越界=%d\n",
		manifestFiles, plan.ExportedCount(), autoOutside)

	if *lstSuggest {
		fmt.Printf("\n[登记建议] %d 条（写入 registry 会改变游戏加载行为，仅提议）\n", len(result.Suggestions))
		for i, item := range result.Suggestions {
			if i >= 10 {
				fmt.Printf("   ... 共 %d\n", len(result.Suggestions))
				break
			}
			fmt.Printf("   %s ← %s 引用（%s）→ 建议登记进 %s\n",
				item.EntryPath, item.ReferencedBy, item.EdgeID, item.ListPath)
		}
	}

	dead := extraction.DeadEdges(catalog, closure.ByEdge)
	fmt.Printf("\n本次未命中的边=%d：%s\n", len(dead), strings.Join(dead, " "))
	if !*quiet {
		fmt.Printf("\n边命中：%s\n", joinCounts(closure.ByEdge))
	}
}

// verifyBytes checks the written form of every exported entry. For raw output
// the bytes must equal the archive payload. For text output the file must be
// valid UTF-8 (the import path rejects anything else) and must re-parse to
// exactly the archive entry's semantic projection — byte equality is not the
// criterion there, because re-encoding renumbers string-pool slots without
// changing a single value (measured: 88% byte-equal, 100% semantically equal on
// 1,497 sampled script entries).
func verifyBytes(archive *pvf.Archive, closure *extraction.Closure, plan *extraction.Plan, out string) (rawBad, textBad, textFiles, rawFiles int) {
	for _, index := range plan.Exported() {
		relative, err := plan.DestPath(index)
		if err != nil {
			rawBad++
			continue
		}
		target := filepath.Join(out, extraction.DefaultSubdir, filepath.FromSlash(relative))
		onDisk, err := os.ReadFile(target)
		if err != nil {
			rawBad++
			continue
		}
		form := extraction.EffectiveForm(archive, index, plan.Selection.Form)
		if form == extraction.FormRaw {
			rawFiles++
			raw, err := archive.RawBytes(index)
			if err != nil || string(onDisk) != string(raw) {
				rawBad++
			}
			continue
		}
		textFiles++
		if !utf8.Valid(onDisk) {
			textBad++
			continue
		}
		want, err := archive.ScriptViewOf(index)
		if err != nil {
			textBad++
			continue
		}
		got := pvf.ParseScriptView(string(onDisk))
		if diff := firstViewDiff(want, got); diff != "" {
			textBad++
			if textBad <= 5 {
				fmt.Printf("   文本语义不一致 %s: %s\n", relative, diff)
			}
		}
	}
	return rawBad, textBad, textFiles, rawFiles
}

// firstViewDiff compares two script projections element by element.
func firstViewDiff(want, got pvf.ScriptView) string {
	if len(want.Elements) != len(got.Elements) {
		return fmt.Sprintf("元素数 %d vs %d", len(want.Elements), len(got.Elements))
	}
	for i := range want.Elements {
		a, b := want.Elements[i], got.Elements[i]
		if a.Kind != b.Kind || a.TokenType != b.TokenType || a.Value != b.Value ||
			a.Section != b.Section || a.SectionID != b.SectionID || a.Index != b.Index {
			return fmt.Sprintf("第 %d 个元素 %q(%d) vs %q(%d)", i, clipForCmd(a.Value), a.TokenType, clipForCmd(b.Value), b.TokenType)
		}
	}
	return ""
}

func clipForCmd(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// verifyManifest checks criterion 5: every manifest entry exists on disk, the
// manifest covers exactly the planned set, and no auto-completed entry escaped
// the _external/ directory. Returning the two counts keeps the check
// falsifiable from the outside.
func verifyManifest(plan *extraction.Plan, out string, manifest extraction.Manifest) (int, int) {
	outside := 0
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		seen[filepath.ToSlash(file.Dest)] = true
		if _, err := os.Stat(filepath.Join(out, extraction.DefaultSubdir, filepath.FromSlash(file.Dest))); err != nil {
			outside++
			continue
		}
		if file.AutoCompleted && !strings.HasPrefix(filepath.ToSlash(file.Dest), extraction.ExternalDir+"/") {
			outside++
		}
	}
	for _, index := range plan.Exported() {
		relative, err := plan.DestPath(index)
		if err != nil || !seen[filepath.ToSlash(relative)] {
			outside++
		}
	}
	return outside, len(manifest.Files)
}

func modeName(strict bool) string {
	if strict {
		return "strict（严格子集，标 partial）"
	}
	return "closed（自动补入，保闭包）"
}

func describeSelection(categories []string) string {
	if len(categories) == 0 {
		return "全部类别"
	}
	return strings.Join(categories, ",")
}

func otherRow(plan *extraction.Plan) (extraction.CategorySummary, bool) {
	for _, row := range plan.Categories {
		if row.ID == extraction.OtherCategory {
			return row, true
		}
	}
	return extraction.CategorySummary{}, false
}

func samplePaths(archive *pvf.Archive) map[string]string {
	out := map[string]string{}
	for index := int32(0); index < archive.FileCount(); index++ {
		p := archive.FullPath(index)
		ext := strings.ToLower(filepath.Ext(p))
		if ext == "" || out[ext] != "" {
			continue
		}
		out[ext] = p
	}
	return out
}

// withExtraRelations supplements the shipped relation definitions with registries
// that exist in real archives but are not declared in config/lists.json yet.
func withExtraRelations(base map[string]annotations.RelationSpec) map[string]annotations.RelationSpec {
	out := make(map[string]annotations.RelationSpec, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	if _, ok := out["APC"]; !ok {
		out["APC"] = annotations.RelationSpec{ListPath: "aicharacter/aicharacter.lst",
			IDToken: 0, PathToken: 1, RecordTokens: 2, NameSection: "name"}
	}
	return out
}

func joinCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if counts[keys[j]] > counts[keys[i]] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "extract:", err)
	os.Exit(1)
}
