package extraction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func planDoc() Document {
	doc := testDocument()
	doc.Categories = []Category{
		{ID: "obj", Name: "物件", Order: 1, Extensions: []string{".obj", ".ptl"}, Relations: []string{"被动对象"}},
		{ID: "ani", Name: "动画", Order: 2, Extensions: []string{".ani", ".til"}},
	}
	return doc
}

// TestBuildPlanKeepsSetClosedAndNavigable covers the default (non-strict)
// selection: the selected set is exported, ancestors come along so the tree is
// navigable, and everything else that the selection references is pulled in as
// auto-completed rather than quietly blended in.
func TestBuildPlanKeepsSetClosedAndNavigable(t *testing.T) {
	doc := planDoc()
	f := newFixtureFrom(t, nil, doc)
	scanner := f.scannerWith(t, doc)
	seed, ok := f.archive.Find("map/forest/a.map")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}
	closure, err := scanner.Closure([]int32{seed}, Options{CollectUnmodeled: true})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure, Selection{Categories: []string{"obj"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var main, auto []string
	for _, index := range plan.Main {
		main = append(main, f.archive.FullPath(index))
	}
	for _, index := range plan.Exported() {
		if plan.AutoCompleted(index) {
			auto = append(auto, f.archive.FullPath(index))
		}
	}
	if len(plan.Main) != 3 {
		t.Errorf("主集应为 种子 + 两个 .obj, got %d: %v", len(plan.Main), main)
	}
	if len(auto) != 2 {
		t.Errorf("自动补入应为 door.ani 与 ground.til, got %d: %v", len(auto), auto)
	}
	for _, index := range plan.Exported() {
		relative, err := plan.DestPath(index)
		if err != nil {
			t.Fatalf("DestPath: %v", err)
		}
		if plan.AutoCompleted(index) && !strings.HasPrefix(relative, ExternalDir+"/") {
			t.Errorf("补入条目必须落在 %s/ 下, got %s", ExternalDir, relative)
		}
		if !plan.AutoCompleted(index) && strings.HasPrefix(relative, ExternalDir+"/") {
			t.Errorf("主集条目不应落在 %s/ 下: %s", ExternalDir, relative)
		}
	}

	// 判据 4：补入之后集合必须封闭。"traversal" 为 0 是硬要求；fixture 里有一条
	// 故意未声明的 [mystery anim]，它属于目录缺口（missing-edge），不是闭包破裂。
	violations, err := VerifyPlan(f.archive, scanner, plan)
	if err != nil {
		t.Fatalf("补入后封闭性失败: %v", err)
	}
	for _, v := range violations {
		if v.Kind != "missing-edge" {
			t.Errorf("补入后仍有遍历违反: %v", v)
		}
	}
}

// TestStrictPlanReportsExactlyItsDanglingRefs pins the strict contract: the
// exported subset deliberately breaks the closure, so the number of traversal
// violations must equal the number of recorded dangling references — no more
// (the plan hides something) and no fewer (the walk found an untracked break).
func TestStrictPlanReportsExactlyItsDanglingRefs(t *testing.T) {
	doc := planDoc()
	f := newFixtureFrom(t, nil, doc)
	scanner := f.scannerWith(t, doc)
	seed, _ := f.archive.Find("map/forest/a.map")
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure, Selection{Categories: []string{"obj"}, Strict: true})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.External) != 0 {
		t.Errorf("strict 模式不得补入条目, got %d", len(plan.External))
	}
	if len(plan.Dangling) == 0 {
		t.Fatal("strict 模式应记录悬空引用")
	}
	if _, err := VerifyPlan(f.archive, scanner, plan); err != nil {
		t.Errorf("strict 模式违反了应等于悬空数的约定: %v", err)
	}
}

// TestManifestAloneRebuildsTheExportedSet is criterion 5: reading manifest.json
// must be enough to reconstruct which entries were written, which of them were
// auto-completed, and under which category.
func TestManifestAloneRebuildsTheExportedSet(t *testing.T) {
	doc := planDoc()
	f := newFixtureFrom(t, nil, doc)
	scanner := f.scannerWith(t, doc)
	seed, _ := f.archive.Find("map/forest/a.map")
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure,
		Selection{Categories: []string{"obj"}, Layout: LayoutCategory})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	root := t.TempDir()
	if _, err := Export(f.archive, closure, ExportOptions{Dir: root, WithReports: true, Plan: plan}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		t.Fatalf("读取 manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest 不是合法 JSON: %v", err)
	}
	if manifest.Selection == nil {
		t.Fatal("manifest 缺少 selection 段")
	}
	written := map[string]ExportedFile{}
	for _, file := range manifest.Files {
		written[filepath.ToSlash(file.Dest)] = file
	}
	if len(written) != plan.ExportedCount() {
		t.Fatalf("manifest 文件数 %d 与计划 %d 不符", len(written), plan.ExportedCount())
	}
	for _, index := range plan.Exported() {
		relative, err := plan.DestPath(index)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := written[filepath.ToSlash(relative)]
		if !ok {
			t.Errorf("manifest 缺少 %s", relative)
			continue
		}
		if entry.AutoCompleted != plan.AutoCompleted(index) {
			t.Errorf("%s 的 autoCompleted 标记不一致", relative)
		}
		if entry.Category != plan.CategoryOf(index) {
			t.Errorf("%s 的类别不一致: %s vs %s", relative, entry.Category, plan.CategoryOf(index))
		}
	}
	if len(manifest.Selection.AutoCompleted) != len(plan.External) {
		t.Errorf("manifest 记录的补入清单 %d 条，计划 %d 条",
			len(manifest.Selection.AutoCompleted), len(plan.External))
	}
	// 补入清单里的每一条都必须真的在 _external/ 下。
	for _, dest := range manifest.Selection.AutoCompleted {
		if !strings.HasPrefix(filepath.ToSlash(dest), ExternalDir+"/") {
			t.Errorf("补入清单条目越界: %s", dest)
		}
	}
}

// TestCategoryDirNamesCannotEscapeTheRoot: category names are editable JSON, so
// a hostile or careless name must not become a path traversal.
func TestCategoryDirNamesCannotEscapeTheRoot(t *testing.T) {
	doc := planDoc()
	doc.Categories[0].Name = "../../escape/evil"
	f := newFixtureFrom(t, nil, doc)
	scanner := f.scannerWith(t, doc)
	seed, _ := f.archive.Find("map/forest/a.map")
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure,
		Selection{Categories: []string{"obj"}, Layout: LayoutCategory})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	root := t.TempDir()
	for _, index := range plan.Exported() {
		if plan.AutoCompleted(index) {
			continue
		}
		relative, err := plan.DestPath(index)
		if err != nil {
			t.Fatalf("DestPath: %v", err)
		}
		if strings.Contains(relative, "..") {
			t.Errorf("类别目录名逃出了根目录: %s", relative)
		}
		target, err := resolveUnder(root, DefaultSubdir, relative)
		if err != nil {
			t.Fatalf("合法相对路径被拒: %v", err)
		}
		if !strings.HasPrefix(target, filepath.Join(root, DefaultSubdir)) {
			t.Errorf("落点越出导出根: %s", target)
		}
	}
}

// TestManifestNeverReportsAnUnmeasuredCoverage pins the honesty of two manifest
// fields. Coverage divides by the candidate queue, so a walk that turned the
// queue off must omit it instead of printing a perfect 1.000; CatalogEdges is
// what makes two exports comparable, and it used to sit at zero unfilled.
func TestManifestNeverReportsAnUnmeasuredCoverage(t *testing.T) {
	f := newFixture(t)
	scanner := f.scanner(t)
	seed, ok := f.archive.Find("map/forest/a.map")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}
	closed, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closed, Selection{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	result, err := Export(f.archive, closed, ExportOptions{
		Dir: t.TempDir(), WithReports: true, Plan: plan, Scanner: scanner})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.Manifest.Coverage != nil {
		t.Errorf("未采集候选队列时不该给出覆盖率: %v", *result.Manifest.Coverage)
	}
	if result.Manifest.VocabCoverage <= 0 || result.Manifest.VocabCoverage > 1 {
		t.Errorf("词汇消费率应可测得: %v", result.Manifest.VocabCoverage)
	}
	if result.Manifest.CatalogEdges != scanner.Catalog().EdgeCount() || result.Manifest.CatalogEdges == 0 {
		t.Errorf("manifest 应记下生效的边目录规模: %d vs %d",
			result.Manifest.CatalogEdges, scanner.Catalog().EdgeCount())
	}

	queued, err := scanner.Closure([]int32{seed}, Options{CollectUnmodeled: true})
	if err != nil {
		t.Fatalf("Closure(queue): %v", err)
	}
	if !queued.UnmodeledCollected {
		t.Fatal("闭包应记下候选队列是否采集")
	}
	plan2, err := BuildPlan(f.archive, scanner.Catalog(), queued, Selection{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	result2, err := Export(f.archive, queued, ExportOptions{
		Dir: t.TempDir(), WithReports: true, Plan: plan2, Scanner: scanner})
	if err != nil {
		t.Fatalf("Export(queue): %v", err)
	}
	if result2.Manifest.Coverage == nil {
		t.Error("采集了候选队列就该给出覆盖率")
	}
}
