package extraction

import (
	"runtime"
	"sort"
	"strings"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

func planScanner(t *testing.T, archive *pvf.Archive) *Scanner {
	t.Helper()
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	specs := map[string]annotations.RelationSpec{
		"map": {ListPath: "map/map.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
	}
	return NewScanner(archive, catalog, NewRegistrySet(archive, specs))
}

// TestPlanPackageCarryAndDangling is E13's contract in one archive pair: what the
// MOD must ship, what it silently borrows from the host baseline, what nothing
// can satisfy, and what it breaks by deleting something a kept file still uses.
func TestPlanPackageCarryAndDangling(t *testing.T) {
	baseline := diffArchive(t, map[string]string{
		"map/map.lst":                 "1\t`forest/a.map`\n",
		"map/forest/a.map":            "[tile]\n\t`tiles/keep.til`\n",
		"map/forest/tiles/keep.til":   "[image]\n\t`k.img`\n\t0\n",
		"map/forest/tiles/legacy.til": "[image]\n\t`l.img`\n\t0\n",
	})
	mod := diffArchive(t, map[string]string{
		"map/map.lst": "1\t`forest/a.map`\n",
		// 一条新依赖、一条只在基线有的依赖、一条两边都没有的依赖。
		"map/forest/a.map":         "[tile]\n\t`tiles/new.til`\n[tile]\n\t`tiles/legacy.til`\n[tile]\n\t`tiles/ghost.til`\n",
		"map/forest/tiles/new.til": "[image]\n\t`n.img`\n\t0\n",
	})
	plan, err := PlanPackage(baseline, mod, planScanner(t, mod), planScanner(t, baseline),
		DiffVerdicts{Modified: []string{"map/forest/a.map"},
			Deleted: []string{"map/forest/tiles/keep.til", "map/forest/tiles/legacy.til"}},
		PackageOptions{Workers: runtime.NumCPU(), ReportBrokenReferences: true})
	if err != nil {
		t.Fatalf("PlanPackage: %v", err)
	}

	if plan.Seeds != 1 {
		t.Errorf("种子数 = %d, want 1", plan.Seeds)
	}
	if !equalStrings(plan.Carry, []string{"map/forest/a.map", "map/forest/tiles/new.til"}) {
		t.Errorf("必带集 = %v", plan.Carry)
	}
	if !equalStrings(plan.Dependencies, []string{"map/forest/tiles/new.til"}) {
		t.Errorf("纯依赖集 = %v（改动的文件不算依赖）", plan.Dependencies)
	}
	if len(plan.SatisfiedByBaseline) != 1 ||
		!strings.Contains(plan.SatisfiedByBaseline[0].Target, "legacy.til") ||
		plan.SatisfiedByBaseline[0].From != "map/forest/a.map" {
		t.Errorf("应报出 1 条由基线满足的依赖（含引用方是谁）, got %+v", plan.SatisfiedByBaseline)
	}
	if len(plan.Unresolvable) != 1 ||
		!strings.Contains(plan.Unresolvable[0].Target, "ghost.til") {
		t.Errorf("应报出 1 条两边都没有的依赖, got %+v", plan.Unresolvable)
	}
	if !equalStrings(plan.BrokenReferences, []string{"map/forest/a.map"}) {
		t.Errorf("MOD 保留了 a.map 却删掉它引用的 legacy.til，应报破坏性引用, got %v", plan.BrokenReferences)
	}
	if plan.ByCategory[OtherCategory] != 0 {
		t.Errorf("该样例不该出现未分类条目, got %v", plan.ByCategory)
	}
	if plan.Bytes <= 0 {
		t.Errorf("必带集字节数应大于 0, got %d", plan.Bytes)
	}
	totals := plan.ExtensionTotals()
	if len(totals) != 2 || totals[0].Count != 1 || totals[1].Count != 1 {
		t.Fatalf("必带集应含两个扩展名各 1 条, got %+v", totals)
	}
	if totals[0].Extension != ".map" || totals[1].Extension != ".til" {
		t.Errorf("计数相同的扩展名应按名字排序, got %+v", totals)
	}
}

// TestPlanPackageScopeAndCaps covers the two guard rails a real MOD author needs:
// a plan limited to one subtree, and a hard cap so a mistaken whole-archive
// change set cannot silently ask for the entire game.
func TestPlanPackageScopeAndCaps(t *testing.T) {
	baseline := diffArchive(t, map[string]string{
		"map/map.lst":            "1\t`forest/a.map`\n2\t`forest/b.map`\n",
		"map/forest/a.map":       "[tile]\n\t`tiles/a.til`\n",
		"map/forest/b.map":       "[tile]\n\t`tiles/b.til`\n",
		"map/forest/tiles/a.til": "[image]\n\t`a.img`\n\t0\n",
		"map/forest/tiles/b.til": "[image]\n\t`b.img`\n\t0\n",
	})
	mod := diffArchive(t, map[string]string{
		"map/map.lst":            "1\t`forest/a.map`\n2\t`forest/b.map`\n",
		"map/forest/a.map":       "[tile]\n\t`tiles/a.til`\n",
		"map/forest/b.map":       "[tile]\n\t`tiles/b.til`\n",
		"map/forest/tiles/a.til": "[image]\n\t`a.img`\n\t0\n",
		"map/forest/tiles/b.til": "[image]\n\t`b.img`\n 9\n",
	})
	verdicts := DiffVerdicts{Modified: []string{"map/forest/a.map", "map/forest/b.map"}}

	scoped, err := PlanPackage(baseline, mod, planScanner(t, mod), planScanner(t, baseline),
		verdicts, PackageOptions{Workers: 1, Scope: []string{"forest/tiles"}})
	if err != nil {
		t.Fatalf("PlanPackage(scope): %v", err)
	}
	if scoped.Seeds != 0 || len(scoped.Carry) != 0 {
		t.Errorf("限定范围外应无可计划条目, got %+v", scoped.Carry)
	}

	full, err := PlanPackage(baseline, mod, planScanner(t, mod), planScanner(t, baseline),
		verdicts, PackageOptions{Workers: 1})
	if err != nil {
		t.Fatalf("PlanPackage(full): %v", err)
	}
	if len(full.Carry) != 4 {
		t.Errorf("两个地图各带一条依赖, got %v", full.Carry)
	}
	capped, err := PlanPackage(baseline, mod, planScanner(t, mod), planScanner(t, baseline),
		verdicts, PackageOptions{Workers: 1, MaxCarry: 2})
	if err != nil {
		t.Fatalf("PlanPackage(capped): %v", err)
	}
	if len(capped.Carry) > 2 {
		t.Errorf("条目上限没起作用: %v", capped.Carry)
	}
	if len(capped.Dependencies) >= len(full.Dependencies) {
		t.Errorf("截断后依赖面应更小: capped=%v full=%v", capped.Dependencies, full.Dependencies)
	}
	if !capped.CarryTruncated || capped.Dropped == 0 {
		t.Errorf("上限截断必须可见，否则残缺计划读起来像完整计划: truncated=%v dropped=%d",
			capped.CarryTruncated, capped.Dropped)
	}
	if full.CarryTruncated {
		t.Errorf("未设上限不该标记截断")
	}
	empty, err := PlanPackage(baseline, mod, planScanner(t, mod), nil, DiffVerdicts{}, PackageOptions{})
	if err != nil {
		t.Fatalf("PlanPackage(empty): %v", err)
	}
	if len(empty.Carry) != 0 || empty.Seeds != 0 {
		t.Errorf("无改动应给出空计划, got %+v", empty)
	}
}

// TestPackagePlanFeedsTheExporter is the E13 hand-off: a plan is only a package
// if the carry set can be handed to the exporter unchanged, and a listed path
// the closure does not hold has to come back as a warning instead of a shrug.
func TestPackagePlanFeedsTheExporter(t *testing.T) {
	baseline := diffArchive(t, map[string]string{
		"map/map.lst":               "1\t`forest/a.map`\n",
		"map/forest/a.map":          "[tile]\n\t`tiles/keep.til`\n",
		"map/forest/tiles/keep.til": "[image]\n\t`k.img`\n\t0\n",
	})
	mod := diffArchive(t, map[string]string{
		"map/map.lst":               "1\t`forest/a.map`\n",
		"map/forest/a.map":          "[tile]\n\t`tiles/new.til`\n",
		"map/forest/tiles/new.til":  "[image]\n\t`n.img`\n\t0\n",
		"map/forest/tiles/gone.til": "[image]\n\t`g.img`\n\t0\n",
	})
	scanner := planScanner(t, mod)
	plan, err := PlanPackage(baseline, mod, scanner, planScanner(t, baseline),
		DiffVerdicts{Modified: []string{"map/forest/a.map", "map/forest/tiles/gone.til"}},
		PackageOptions{Workers: 1})
	if err != nil {
		t.Fatalf("PlanPackage: %v", err)
	}
	if plan.Closure == nil {
		t.Fatal("计划必须带着闭包，否则导出侧无法接手")
	}
	listed := append(append([]string{}, plan.Carry...), "map/forest/tiles/nowhere.til")
	built, err := BuildPlan(mod, scanner.Catalog(), plan.Closure, Selection{Paths: listed})
	if err != nil {
		t.Fatalf("BuildPlan(paths): %v", err)
	}
	if len(built.UnlistedPaths) != 1 ||
		!strings.Contains(built.UnlistedPaths[0], "nowhere.til") {
		t.Errorf("白名单里闭包没有的路径必须报出来, got %v", built.UnlistedPaths)
	}
	got := make([]string, 0, len(built.Main))
	for _, index := range built.Main {
		got = append(got, mod.FullPath(index))
	}
	// Main 按条目号有序、Carry 按路径有序，比较只该看集合。
	sort.Strings(got)
	want := append([]string{}, plan.Carry...)
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Errorf("按路径勾选的结果应等于必带集:\n got %v\nwant %v", got, want)
	}
	// gone.til 是 MOD 里存在但没人引用的新文件，它照样是必带项（改动即必带）。
	if !strings.Contains(strings.Join(plan.Carry, " "), "gone.til") {
		t.Errorf("新增文件即使不被引用也要进必带集, got %v", plan.Carry)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for at := range got {
		if got[at] != want[at] {
			return false
		}
	}
	return true
}
