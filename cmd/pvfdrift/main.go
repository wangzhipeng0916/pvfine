// Command pvfdrift compares two Script.pvf files: which entries exist, which
// payloads differ, and — the part no other tool in this repo does — which
// references appeared or disappeared between the two revisions. With -plan the
// same comparison is read as "baseline vs MOD" and turned into a package plan.
//
// It is a development instrument, not a shipped feature, and it writes nothing:
//
//	PVF_TESTFILE=... PVF_TESTFILE2=... go run ./cmd/pvfdrift -top 20
//	PVF_TESTFILE=... PVF_TESTFILE2=... go run ./cmd/pvfdrift -plan -scope dungeon -broken
package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"time"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	var scopes stringList
	oldPath := flag.String("old", os.Getenv("PVF_TESTFILE"), "基线 Script.pvf 路径，默认取 PVF_TESTFILE")
	newPath := flag.String("new", os.Getenv("PVF_TESTFILE2"), "对照 Script.pvf 路径，默认取 PVF_TESTFILE2")
	catalogPath := flag.String("catalog", "", "自定义边目录；缺省用内置（两边必须同一份声明才可比）")
	all := flag.Bool("all", false, "对所有两边都有的条目比对引用（默认只比对内容变了的条目）")
	plan := flag.Bool("plan", false, "把 diff 当作「基线 vs MOD」，输出必带集、由基线满足的依赖与破坏性引用")
	broken := flag.Bool("broken", false, "配合 -plan：再跑一次基线全档反向扫描，找出仍引用被删条目的文件（慢）")
	maxCarry := flag.Int("max-carry", 0, "配合 -plan：必带集条目上限，0 为不限")
	exportDir := flag.String("export", "", "配合 -plan：把必带集按原文本导出到该目录（开发验证用，不写进仓库）")
	top := flag.Int("top", 20, "引用漂移汇总输出条数")
	listTop := flag.Int("list", 12, "条目层每类最多列出多少路径")
	flag.Var(&scopes, "scope", "只处理路径含该前缀的条目，可重复")
	quiet := flag.Bool("quiet", false, "只输出结论")
	flag.Parse()

	if *oldPath == "" || *newPath == "" {
		fmt.Fprintln(os.Stderr, "pvfdrift: 需要 -old 与 -new（或 PVF_TESTFILE / PVF_TESTFILE2）")
		os.Exit(2)
	}
	if *oldPath == *newPath {
		fmt.Fprintln(os.Stderr, "pvfdrift: 两个路径相同，比较无意义")
		os.Exit(2)
	}
	catalogData, err := readOptional(*catalogPath, extraction.EmbeddedEdges)
	if err != nil {
		fail(err)
	}
	catalog, err := extraction.LoadCatalog(catalogData)
	if err != nil {
		fail(err)
	}
	specs, err := relationSpecs()
	if err != nil {
		fail(err)
	}

	left, right := open(*oldPath), open(*newPath)
	defer left.Release()
	defer right.Release()
	fmt.Printf("基线 %s：条目=%d version=%s format=%s\n对照 %s：条目=%d version=%s format=%s\n",
		path.Base(*oldPath), left.FileCount(), left.ClientVersion(), left.Format(),
		path.Base(*newPath), right.FileCount(), right.ClientVersion(), right.Format())

	started := time.Now()
	entries, err := extraction.CompareEntries(left, right, runtime.NumCPU())
	if err != nil {
		fail(err)
	}
	fmt.Printf("\n条目层（%.1fs）：两边相同=%d（池重排 %d）内容变了=%d 只在基线=%d 只在对照=%d\n",
		time.Since(started).Seconds(), entries.Same, entries.PoolRenumbered, len(entries.Changed),
		len(entries.OnlyLeft), len(entries.OnlyRight))
	if !*quiet {
		printGrouped("内容变了", entries.Changed, *listTop)
		printGrouped("只在基线", entries.OnlyLeft, *listTop)
		printGrouped("只在对照", entries.OnlyRight, *listTop)
	}

	shared := extraction.ScopePaths(entries.Changed, scopes)
	if *all {
		shared = extraction.ScopePaths(sharedEntries(left, right), scopes)
	}
	leftScanner := extraction.NewScanner(left, catalog, extraction.NewRegistrySet(left, specs))
	rightScanner := extraction.NewScanner(right, catalog, extraction.NewRegistrySet(right, specs))
	warm := time.Now()
	if err := leftScanner.Prewarm(nil); err != nil {
		fail(err)
	}
	if err := rightScanner.Prewarm(nil); err != nil {
		fail(err)
	}

	if *plan {
		runPlan(left, right, leftScanner, rightScanner, catalog, entries, scopes,
			*broken, *maxCarry, *exportDir, *quiet)
		return
	}
	if len(shared) == 0 {
		fmt.Println("\n没有可比对的共同条目，引用层跳过")
		return
	}

	fmt.Printf("\n引用层：待比对 %d 条（%s，两边共用同一份边目录 %d 条边；预热 %s）\n",
		len(shared), map[bool]string{true: "全部共同条目", false: "内容变了的条目"}[*all],
		catalog.EdgeCount(), time.Since(warm).Round(time.Millisecond))
	scanStarted := time.Now()
	drifts, err := extraction.CompareReferences(leftScanner, rightScanner, shared, func(done, total int) {
		if *quiet || done%20000 != 0 {
			return
		}
		fmt.Printf("  ... %d/%d elapsed=%s\n", done, total, time.Since(scanStarted).Round(time.Second))
	})
	if err != nil {
		fail(err)
	}
	added, dropped := 0, 0
	for _, drift := range drifts {
		added += len(drift.Added)
		dropped += len(drift.Dropped)
	}
	fmt.Printf("引用漂移：受影响条目=%d 新增引用=%d 消失引用=%d 耗时=%s\n",
		len(drifts), added, dropped, time.Since(scanStarted).Round(time.Millisecond))

	summaries := extraction.SummarizeDrift(drifts)
	fmt.Printf("\n按边汇总（前 %d 类，共 %d 类）：\n", *top, len(summaries))
	for at, row := range summaries {
		if at >= *top {
			break
		}
		fmt.Printf("  %-28s %-26s +%-7d -%-7d 条目=%-7d 例: %s\n",
			row.EdgeID, row.Section, row.Added, row.Dropped, row.Files, row.Example)
	}
	if *quiet {
		return
	}
	for at, drift := range drifts {
		if at >= 8 {
			break
		}
		fmt.Printf("\n  %s（相同 %d 条引用）\n", drift.Path, drift.Same)
		for _, link := range first(drift.Added, 4) {
			fmt.Printf("    + %-24s %-10s %q → %s\n", link.EdgeID, link.State, link.Value, link.Target)
		}
		for _, link := range first(drift.Dropped, 4) {
			fmt.Printf("    - %-24s %-10s %q → %s\n", link.EdgeID, link.State, link.Value, link.Target)
		}
	}
}

// runPlan reads the diff as "baseline vs MOD" and prints E13's answer: what the
// MOD must carry, what it silently borrows from the host baseline, what nothing
// can satisfy, and what it breaks by deleting something a kept file references.
func runPlan(baseline, mod *pvf.Archive, baselineScanner, modScanner *extraction.Scanner,
	catalog *extraction.Catalog, entries extraction.EntryDiff, scopes []string,
	broken bool, maxCarry int, exportDir string, quiet bool) {
	verdicts := extraction.DiffVerdicts{
		Modified: append(append([]string{}, entries.Changed...), entries.OnlyRight...),
		Deleted:  entries.OnlyLeft,
	}
	started := time.Now()
	result, err := extraction.PlanPackage(baseline, mod, modScanner, baselineScanner, verdicts,
		extraction.PackageOptions{Workers: runtime.NumCPU(), Scope: scopes,
			MaxCarry: maxCarry, ReportBrokenReferences: broken})
	if err != nil {
		fail(err)
	}
	fmt.Printf("\nMOD 计划（scope=%v，%.1fs）：改动或新增=%d 必带=%d 其中纯依赖=%d 约 %.1f MiB\n",
		scopes, time.Since(started).Seconds(), result.Seeds, len(result.Carry),
		len(result.Dependencies), float64(result.Bytes)/1e6)
	if result.CarryTruncated {
		fmt.Printf("  ⚠ 必带集被 -max-carry 截断：还有 %d 条可达条目没展开，下面的数字只是样本\n", result.Dropped)
	}
	categories := make([]string, 0, len(result.ByCategory))
	for id := range result.ByCategory {
		categories = append(categories, id)
	}
	sort.Strings(categories)
	fmt.Print("  按类别：")
	for _, id := range categories {
		fmt.Printf(" %s=%d", id, result.ByCategory[id])
	}
	fmt.Println()
	fmt.Printf("  按扩展名：%v\n", result.ExtensionTotals())
	fmt.Printf("  由基线满足的依赖=%d 两边都缺=%d\n",
		len(result.SatisfiedByBaseline), len(result.Unresolvable))
	for at, row := range result.SatisfiedByBaseline {
		if at >= 6 {
			fmt.Printf("    ... 共 %d\n", len(result.SatisfiedByBaseline))
			break
		}
		fmt.Printf("    基线满足 %s --%s--> %s\n", row.From, row.EdgeID, row.Target)
	}
	for at, row := range result.Unresolvable {
		if at >= 6 {
			fmt.Printf("    ... 共 %d\n", len(result.Unresolvable))
			break
		}
		fmt.Printf("    两边都缺 %s --%s--> %s\n", row.From, row.EdgeID, row.Target)
	}
	if broken {
		fmt.Printf("  破坏性引用（MOD 留着引用方却删了被引用项）=%d\n", len(result.BrokenReferences))
		for at, entryPath := range result.BrokenReferences {
			if at >= 10 {
				fmt.Printf("    ... 共 %d\n", len(result.BrokenReferences))
				break
			}
			fmt.Printf("    %s\n", entryPath)
		}
	}
	if exportDir != "" {
		writePackage(mod, modScanner, catalog, result, exportDir)
	}
	if quiet || len(result.Carry) == 0 {
		return
	}
	fmt.Print("  必带样例：")
	for at := 0; at < 6 && at < len(result.Carry); at++ {
		fmt.Printf(" %s", path.Base(result.Carry[at]))
	}
	fmt.Println()
}

// sharedEntries lists every path both archives hold, which is what -all needs:
// CompareEntries only keeps the differences, and "unchanged" is a count, not a
// list, because listing 40k identical paths would cost more than it tells.
func sharedEntries(left, right *pvf.Archive) []string {
	out := make([]string, 0, left.FileCount())
	for index := int32(0); index < left.FileCount(); index++ {
		entryPath := left.FullPath(index)
		if _, ok := right.Find(entryPath); ok {
			out = append(out, entryPath)
		}
	}
	return out
}

// writePackage turns the carry set into files, because a plan nobody can write
// out is a report rather than a package. The tree is verified against its own
// manifest first: "closed" has to be checked, not assumed.
func writePackage(mod *pvf.Archive, modScanner *extraction.Scanner, catalog *extraction.Catalog,
	result *extraction.PackagePlan, dir string) {
	if result.Closure == nil || len(result.Carry) == 0 {
		fmt.Println("  必带集为空，导出跳过")
		return
	}
	plan, err := extraction.BuildPlan(mod, catalog, result.Closure, extraction.Selection{
		Paths: result.Carry, Layout: extraction.LayoutMirror, Form: extraction.FormText})
	if err != nil {
		fail(err)
	}
	if len(plan.UnlistedPaths) > 0 {
		sample := plan.UnlistedPaths
		if len(sample) > 3 {
			sample = sample[:3]
		}
		fmt.Printf("  ⚠ 必带集有 %d 条路径不在闭包内（不会写出）：%v\n", len(plan.UnlistedPaths), sample)
	}
	violations, err := extraction.VerifyPlan(mod, modScanner, plan)
	if err != nil {
		fail(err)
	}
	written, err := extraction.Export(mod, result.Closure, extraction.ExportOptions{
		Dir: dir, WithReports: true, Plan: plan, Scanner: modScanner})
	if err != nil {
		fail(err)
	}
	fmt.Printf("  导出 %s：文件=%d（文本 %d / 原始 %d）字节=%d 登记表片段=%d 封闭性违规=%d\n",
		written.Dir, written.Files, written.TextFiles, written.RawFiles, written.Bytes,
		len(written.FragmentFiles), len(violations))
	if len(violations) == 0 {
		return
	}
	// 按"缺哪条边"聚合成可执行的工单行：引用方扩展名 + section + 目标扩展名，
	// 正是边目录声明 Edge 所需的三个键；倒 332 行路径没人能据此补边。
	type gapRow struct {
		count int
		from  string
		value string
	}
	byClass := map[string]*gapRow{}
	for _, v := range violations {
		key := v.Kind + " "
		if v.Ref.EdgeID != "" {
			key += v.Ref.EdgeID
		} else {
			key += fmt.Sprintf("%s [%s] → %s", path.Ext(v.From), v.Ref.Section, path.Ext(v.To))
		}
		row := byClass[key]
		if row == nil {
			row = &gapRow{from: v.From, value: v.Ref.Value}
			byClass[key] = row
		}
		row.count++
	}
	names := make([]string, 0, len(byClass))
	for key := range byClass {
		names = append(names, key)
	}
	sort.Slice(names, func(i, j int) bool {
		if byClass[names[i]].count != byClass[names[j]].count {
			return byClass[names[i]].count > byClass[names[j]].count
		}
		return names[i] < names[j]
	})
	for at, key := range names {
		if at >= 40 {
			fmt.Printf("    ... 共 %d 类（-top 只影响引用漂移汇总）\n", len(names))
			break
		}
		fmt.Printf("    %-46s ×%-5d 例 %s ← %q\n", key, byClass[key].count,
			byClass[key].from, byClass[key].value)
	}
}

func open(target string) *pvf.Archive {
	archive, err := pvf.Open(target)
	if err != nil {
		fail(fmt.Errorf("打开 %s 失败: %w", path.Base(target), err))
	}
	return archive
}

// printGrouped shows where the differences live, because a flat path list hides
// whether drift is one directory or the whole archive.
func printGrouped(label string, paths []string, limit int) {
	if len(paths) == 0 {
		return
	}
	counts := map[string]int{}
	for _, entryPath := range paths {
		dir := path.Dir(entryPath)
		if slash := strings.Index(dir, "/"); slash > 0 {
			dir = dir[:slash]
		}
		counts[strings.ToLower(dir)]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	fmt.Printf("  %s %d 条，按顶层目录：", label, len(paths))
	for at, name := range names {
		if at >= 6 {
			fmt.Printf(" …共 %d 个目录", len(names))
			break
		}
		fmt.Printf(" %s=%d", name, counts[name])
	}
	fmt.Println()
	for at, entryPath := range paths {
		if at >= limit/2 {
			break
		}
		fmt.Printf("      例 %s\n", entryPath)
	}
}

func first(links []extraction.RefLink, limit int) []extraction.RefLink {
	if len(links) <= limit {
		return links
	}
	return links[:limit]
}

func relationSpecs() (map[string]annotations.RelationSpec, error) {
	lists, err := annotations.ParseLists(config.ListsJSON)
	if err != nil {
		return nil, err
	}
	specs := make(map[string]annotations.RelationSpec, len(lists.Relations)+1)
	for name, spec := range lists.Relations {
		specs[name] = spec
	}
	// config/lists.json 尚未登记 aicharacter/aicharacter.lst（实测 86 存在），
	// 与其它 CLI harness 用同一套补项，保持几条路径互相可比。
	if _, ok := specs["APC"]; !ok {
		specs["APC"] = annotations.RelationSpec{
			ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1,
			RecordTokens: 2, NameSection: "name",
		}
	}
	return specs, nil
}

func readOptional(explicit string, fallback []byte) ([]byte, error) {
	if explicit == "" {
		return fallback, nil
	}
	return os.ReadFile(explicit)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "pvfdrift:", err)
	os.Exit(1)
}
