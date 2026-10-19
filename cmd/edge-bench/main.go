// Command edge-bench measures the extraction reference graph: closure size,
// catalog coverage and the pending unmodeled-reference queue.
//
// It is a development instrument, not a shipped feature. Typical use while
// growing internal/extraction/edges/extract-edges.json:
//
//	go run ./cmd/edge-bench -seed dungeon/act1/grakkarak.dgn
//	go run ./cmd/edge-bench -all -top 40
//
// The archive path comes from -pvf or PVF_TESTFILE; no local path is embedded,
// and the command never writes to the archive.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

type seedList []string

func (s *seedList) String() string { return strings.Join(*s, ",") }

func (s *seedList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	var seeds seedList
	var targets seedList
	archivePath := flag.String("pvf", os.Getenv("PVF_TESTFILE"), "Script.pvf 路径，默认取 PVF_TESTFILE")
	catalogPath := flag.String("catalog", "", "自定义边目录 JSON；缺省用内置 internal/extraction/edges/extract-edges.json")
	listPath := flag.String("lists", "", "自定义 relation 定义；缺省用内置 config/lists.json")
	flag.Var(&seeds, "seed", "归档内种子路径，可重复")
	depth := flag.Int("depth", 0, "最大引用深度，0 为不限")
	maxEntries := flag.Int("max", 0, "最大条目数，0 为不限")
	all := flag.Bool("all", false, "改为全归档扫描，报告全局覆盖率与未声明引用排行")
	flag.Var(&targets, "reverse", "建全归档反向索引并回答「谁引用它 / 改动波及谁」，可重复")
	orphans := flag.Int("orphans", 0, "建全归档反向索引并输出孤儿内容统计与前 N 个例子")
	suggest := flag.Int("suggest", 0, "从未声明队列里输出 N 条可粘贴的边建议（-all 与 -seed 两条路径都可用）")
	top := flag.Int("top", 25, "未声明引用排行输出条数")
	includeNumeric := flag.Bool("numeric", false, "在未声明队列里显示数值型候选（实测几乎全是 delay/weight/坐标噪声）")
	workers := flag.Int("workers", runtime.NumCPU(), "全归档扫描的并发度；1 为单线程（用于与历史基线对比）")
	verbose := flag.Bool("v", false, "打印每个种子的闭包明细")
	edgesN := flag.Int("edges", 0, "输出每条已声明边的实测值数/解析/缺失/未知排行（最差解析率在前），0 为不输出")
	rows := flag.String("rows", "", "只输出某个 relation 登记表的前 N 行（ID<TAB>路径），用于取真实种子样本")
	rowsN := flag.Int("n", 12, "-rows 的输出行数上限")
	flag.Parse()

	if *archivePath == "" {
		fmt.Fprintln(os.Stderr, "edge-bench: 需要 -pvf 或环境变量 PVF_TESTFILE")
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
	listData, err := readOptional(*listPath, config.ListsJSON)
	if err != nil {
		fail(err)
	}
	lists, err := annotations.ParseLists(listData)
	if err != nil {
		fail(err)
	}

	started := time.Now()
	archive, err := pvf.Open(*archivePath)
	if err != nil {
		fail(err)
	}
	specs := mergeRelations(lists.Relations, extraRelations())
	scanner := extraction.NewScanner(archive, catalog, extraction.NewRegistrySet(archive, specs))
	fmt.Printf("archive=%s entries=%d format=%s catalogEdges=%d relations=%d open=%s\n",
		*archivePath, archive.FileCount(), archive.Format(), catalog.EdgeCount(), len(specs),
		time.Since(started).Round(time.Millisecond))

	if *rows != "" {
		reportRows(scanner, *rows, *rowsN)
		return
	}
	if len(targets) > 0 || *orphans > 0 {
		runReverse(context.Background(), archive, scanner, *workers, targets, *orphans, *top)
		return
	}
	if *all {
		fullScan(context.Background(), archive, scanner, catalog, *top, *includeNumeric, *workers, *suggest, *edgesN)
		return
	}
	if len(seeds) == 0 {
		fmt.Fprintln(os.Stderr, "edge-bench: 至少需要一个 -seed，或用 -all")
		os.Exit(2)
	}
	aggregate := extraction.NewAggregator()
	elapsed := time.Duration(0)
	for _, seed := range seeds {
		// 走 ResolveSeed 而不是只认路径：预设的说明里写的是「关系名:ID」，
		// 量这些预设时工具必须接受同一种写法。
		hit, err := extraction.ResolveSeed(archive, scanner.RegistrySet(), seed)
		if err != nil {
			fmt.Printf("!! %v\n", err)
			continue
		}
		index := hit.Index
		round := time.Now()
		closure, err := scanner.Closure([]int32{index}, extraction.Options{
			MaxEntries: *maxEntries, MaxDepth: *depth, CollectUnmodeled: true,
		})
		if err != nil {
			fmt.Printf("!! 闭包失败 %s: %v\n", seed, err)
			continue
		}
		took := time.Since(round)
		elapsed += took
		fmt.Printf("\n== %s ==\n", seed)
		if hit.Via != "path" {
			fmt.Printf("   种子经 %s(%s) 解析为 %s\n", hit.Via, hit.Relation, hit.Path)
		}
		fmt.Printf("条目=%d 引用状态=%v 覆盖率=%.4f 耗时=%s 每条目=%.3fms\n",
			len(closure.Nodes), closure.States, closure.Coverage(), took.Round(time.Millisecond),
			float64(took.Nanoseconds())/1e6/float64(max(len(closure.Nodes), 1)))
		fmt.Printf("扩展名分布: %s\n", formatCounts(closure.Extensions()))
		fmt.Printf("边命中: %s\n", formatCounts(closure.ByEdge))
		aggregate.Add(closure)
		if *verbose {
			for _, node := range closure.Nodes {
				fmt.Printf("   d%d %-72s via %-26s %q\n", node.Depth, node.Path, node.Via.EdgeID, node.Via.Value)
			}
			// 未解析引用要打 closure.Unresolved：closure.Edges 只留可跟随的边，
			// 从它里面筛 missing 永远筛不出东西（同 modset.go 的逐条目重扫理由）。
			for _, ref := range closure.Unresolved {
				if ref.State == extraction.StateUnknown {
					continue
				}
				fmt.Printf("   %s [%s] %-26s %q → 试过 %s（%s）\n",
					ref.State, ref.Section, ref.EdgeID, ref.Value, ref.TargetPath, ref.Note)
			}
		}
	}
	fmt.Printf("\n合计: 条目=%d 覆盖率=%.4f path 形缺口=%d 耗时=%s registry懒加载=%d 回退命中=%d\n",
		aggregate.Nodes, aggregate.Coverage(), aggregate.PathGapCount(), elapsed.Round(time.Millisecond),
		scanner.RegistrySet().Loads(), scanner.RegistrySet().Fallbacks)
	reportEdges(aggregate, *edgesN)
	reportQueue(aggregate.Rows(), *top, *includeNumeric)
	reportSuggestions(aggregate.Rows(), catalog, *suggest)
}

// reportRows prints a slice of one registry so preset work has real seed paths to
// point at. Every preset needs samples, and reading rows out of the archive is the
// only way to get paths that actually resolve — the shipped `.lst` files are not
// unpacked anywhere else on disk.
func reportRows(scanner *extraction.Scanner, relation string, limit int) {
	byPath, err := scanner.RegistrySet().PathIndex(relation)
	if err != nil {
		fmt.Printf("!! 读登记表 %q 失败: %v\n", relation, err)
		fmt.Printf("   可用关系: %s\n", strings.Join(scanner.RegistrySet().Relations(), "、"))
		return
	}
	type row struct{ id, entryPath string }
	rows := make([]row, 0, len(byPath))
	for entryPath, id := range byPath {
		rows = append(rows, row{id: id, entryPath: entryPath})
	}
	sort.Slice(rows, func(i, j int) bool {
		if len(rows[i].id) != len(rows[j].id) {
			return len(rows[i].id) < len(rows[j].id)
		}
		return rows[i].id < rows[j].id
	})
	shown := 0
	for _, line := range rows {
		if shown >= limit {
			break
		}
		fmt.Printf("%s\t%s\n", line.id, line.entryPath)
		shown++
	}
	fmt.Printf("# 登记表 %q 共 %d 行，此处取前 %d 行（ID 数值序）\n", relation, len(rows), shown)
}

// fullScan walks every script entry so coverage is measured against the whole
// archive rather than one closure. This doubles as the eager-mode cost probe.
func fullScan(ctx context.Context, archive *pvf.Archive, scanner *extraction.Scanner, catalog *extraction.Catalog,
	top int, includeNumeric bool, workers, suggestN, edgesN int) {
	aggregate := extraction.NewAggregator()
	indexes := make([]int32, 0, archive.FileCount())
	for index := int32(0); index < archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	warm := time.Now()
	if err := scanner.Prewarm(ctx); err != nil {
		fail(err)
	}
	warmElapsed := time.Since(warm)

	started := time.Now()
	var mu sync.Mutex
	stats, err := scanner.ScanIndices(ctx, indexes, workers, func(result extraction.ScanResult) {
		mu.Lock()
		aggregate.AddScan(result)
		mu.Unlock()
	})
	if err != nil {
		fail(err)
	}
	elapsed := time.Since(started)
	fmt.Printf("预热（basename 索引 + 全部 registry）=%s workers=%d\n", warmElapsed.Round(time.Millisecond), workers)
	fmt.Printf("\n全归档扫描完成 visited=%d textErr=%d 耗时=%s (%.0f 条目/s) 解码字节=%d\n",
		aggregate.Nodes, stats.Failed, elapsed.Round(time.Millisecond),
		float64(aggregate.Nodes)/elapsed.Seconds(), aggregate.TextBytes)
	fmt.Printf("引用状态合计=%v 覆盖率=%.4f path 形缺口=%d\n",
		aggregate.States, aggregate.Coverage(), aggregate.PathGapCount())
	fmt.Printf("边命中: %s\n", formatCounts(aggregate.ByEdge))
	reportEdges(aggregate, edgesN)
	reportQueue(aggregate.Rows(), top, includeNumeric)
	reportSuggestions(aggregate.Rows(), catalog, suggestN)
}

// reportEdges prints the per-edge measured behaviour. This is what makes a batch
// of new declarations reviewable in one run: an edge that fires often but almost
// never lands is a mis-declaration, and ByEdge (resolved only) cannot show that.
func reportEdges(aggregate *extraction.Aggregator, limit int) {
	if limit <= 0 {
		return
	}
	rows := aggregate.EdgeStats()
	kept := rows
	if limit < len(kept) {
		kept = kept[:limit]
	}
	fmt.Printf("\n按边实测（%d 条，最差解析率在前；共 %d 条边有产出）:\n", len(kept), len(rows))
	for _, row := range kept {
		// Unknown 拆成三档显示：同名歧义要改声明或加 roots，动态模板是天生判不了，
		// 而"其他"主要是解析到条目但扩展名不在 expectExt 内——那是边声明过窄的信号。
		// 原先这一列只印 Unknown 却标成"歧义"，差点让我把 12 万条模板值读成同名冲突。
		other := row.Unknown - row.Ambiguous - row.Dynamic
		fmt.Printf("   %-34s 值=%-8d 解析=%-8d 缺失=%-7d 未知=%-7d（同名 %d / 动态 %d / 其他 %d） 解析率=%.3f\n",
			row.EdgeID, row.Values, row.Resolved, row.Missing, row.Unknown,
			row.Ambiguous, row.Dynamic, other, row.Rate())
		if row.MissingSample != "" || row.UnknownSample != "" {
			fmt.Printf("        缺失例 %q  未知例 %q\n", row.MissingSample, row.UnknownSample)
		}
	}
}

// runReverse builds the whole-archive reverse index and answers the reachability
// questions: who references a target, what an edit or deletion would touch, and
// what content nothing references at all.
func runReverse(ctx context.Context, archive *pvf.Archive, scanner *extraction.Scanner, workers int,
	targets []string, orphanN, top int) {
	indexes := make([]int32, 0, archive.FileCount())
	for index := int32(0); index < archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	reverse, err := extraction.BuildReverseIndex(ctx, scanner, indexes, workers)
	if err != nil {
		fail(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("反向索引：扫描=%d 失败=%d 原始链接=%d 去重后=%d（省 %.1f%%）覆盖条目=%d 建索引=%s (workers=%d)\n",
		reverse.Universe, reverse.Failures, reverse.RawLinks, reverse.Refs,
		100*(1-float64(reverse.Refs)/float64(reverse.RawLinks)), reverse.Entries,
		reverse.ScanElapsed.Round(time.Millisecond), workers)
	fmt.Printf("索引自身估算=%.0f MB；进程堆此刻=%.0f MB（后者主要是归档分块缓存，不是索引）\n",
		float64(reverse.BytesEstimate())/1e6, float64(mem.HeapAlloc)/1e6)

	for _, targetPath := range targets {
		index, ok := archive.Find(targetPath)
		if !ok {
			fmt.Printf("!! 条目不存在: %s\n", targetPath)
			continue
		}
		links := reverse.Incoming(index)
		fmt.Printf("\n== %s == 入边=%d\n", targetPath, len(links))
		for at, link := range links {
			if at >= top {
				fmt.Printf("   ... 共 %d\n", len(links))
				break
			}
			fmt.Printf("   [%s] %s\n", kindName(link.Kind), reverse.Explain(scanner, index, link))
		}
		hits := reverse.Ancestors(index, 0)
		byDepth := map[int]int{}
		for _, hit := range hits {
			byDepth[hit.Depth]++
		}
		fmt.Printf("传递祖先=%d（按跳数 %v）\n", len(hits), byDepth)
	}

	if orphanN > 0 {
		orphans, rows := reverse.Orphans()
		fmt.Printf("\n孤儿候选（既无入边也无登记）=%d 条目，占全档 %.1f%%，扩展名排行：\n",
			len(orphans), 100*float64(len(orphans))/float64(archive.FileCount()))
		for at, row := range rows {
			if at >= 10 {
				break
			}
			fmt.Printf("   %-8s 条目=%-8d 字节=%-12d\n", row.Extension, row.Count, row.Bytes)
		}
		// 光报"多少条目没人引用"会把结论卖过头：其中一部分只要补上未声明的边就不
		// 是孤儿，另一部分确实有人提到但解析失败。归因要用同一批扫描数据拆开。
		attribution, err := extraction.AttributeOrphans(ctx, scanner, reverse, workers)
		if err != nil {
			fail(err)
		}
		fmt.Printf("\n孤儿归因（总数=%d，仅被客户端资产引用的条目另有 %d）：\n",
			attribution.Total, attribution.AssetOnly)
		for _, bucket := range []extraction.OrphanBucket{
			extraction.OrphanUndeclaredEdge, extraction.OrphanTemplateNamed,
			extraction.OrphanFailedRef, extraction.OrphanUnexplained} {
			fmt.Printf("   %-16s %8d（%.1f%%）\n", bucket, attribution.Buckets[bucket],
				100*float64(attribution.Buckets[bucket])/float64(max(1, attribution.Total)))
		}
		fmt.Printf("   按扩展名：\n")
		for at, row := range attribution.Rows() {
			if at >= 10 {
				break
			}
			fmt.Printf("     %-8s 未声明边=%-8d 模板=%-8d 解析失败=%-8d 无人引用=%-8d\n",
				row.Extension, row.UndeclaredEdge, row.TemplateNamed, row.FailedRef, row.Unexplained)
		}
		fmt.Printf("   可用动态模板=%d（字面量为空而剔除 %d），配对比较次数=%d\n",
			attribution.Templates, attribution.Degenerate, attribution.Comparisons)
		samples := attribution.Samples(scanner, 2, 12)
		exts := make([]string, 0, len(samples))
		for ext := range samples {
			exts = append(exts, ext)
		}
		sort.Strings(exts)
		for _, ext := range exts {
			fmt.Printf("   无人引用例 %s: %v\n", ext, samples[ext])
		}
	}
}

func kindName(kind uint8) string {
	switch kind {
	case extraction.IncomingEdge:
		return "边"
	case extraction.IncomingAsset:
		return "资产"
	case extraction.IncomingRegistry:
		return "登记"
	case extraction.IncomingList:
		return "表本体"
	}
	return "?"
}

// reportSuggestions turns the top of the unmodeled queue into either a pasteable
// edge fragment or an explicit "a human has to decide" line.
func reportSuggestions(rows []extraction.UnmodeledRow, catalog *extraction.Catalog, limit int) {
	if limit <= 0 {
		return
	}
	items := extraction.SuggestRows(rows, catalog, limit)
	fmt.Printf("\n边建议（%d 条，按置信档与出现次数排序）:\n", len(items))
	pasteable := 0
	for _, item := range items {
		if item.Edge != nil {
			pasteable++
		}
		fmt.Printf("   %-14s %-10s %-8s %-30s 次数=%d\n", item.Confidence, item.Gap.Extension,
			item.Gap.Shape, item.Gap.Section, item.Gap.Count)
		fmt.Printf("        %s\n", item.Reason)
	}
	data, err := extraction.MarshalSuggestions(items)
	if err != nil {
		fail(err)
	}
	fmt.Printf("\n可直接并入 internal/extraction/edges/extract-edges.json 的片段（%d 条可粘贴）:\n%s",
		pasteable, string(data))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func reportQueue(rows []extraction.UnmodeledRow, top int, includeNumeric bool) {
	// 实测表明数值型候选几乎全是 delay/weight/坐标，默认只呈现高置信的路径型缺口。
	hidden := 0
	kept := rows[:0:0]
	for _, row := range rows {
		if row.Shape != "path" {
			hidden++
			if !includeNumeric {
				continue
			}
		}
		kept = append(kept, row)
	}
	if len(kept) == 0 {
		fmt.Printf("\n未声明的路径型引用为空：边目录已覆盖全部可辨识文件引用位。（另有 %d 类数值型候选，-numeric 查看）\n", hidden)
		return
	}
	limit := top
	if limit > len(kept) {
		limit = len(kept)
	}
	fmt.Printf("\n未声明引用队列（路径型 %d 类，显示前 %d；数值型 %d 类%s）\n",
		len(kept), limit, hidden, map[bool]string{true: "，已一并显示", false: "，默认折叠"}[includeNumeric])
	for _, row := range kept[:limit] {
		fmt.Printf("   %8d  %-6s %-10s %-34s 例: %q\n",
			row.Count, row.Shape, row.Extension, row.Section, row.Example)
	}
}

// extraRelations supplements config/lists.json with registries that exist in
// the archive but are not declared there yet. The shipped catalog refers to APC,
// so the bench resolves it without editing the shipped list definitions.
func extraRelations() map[string]annotations.RelationSpec {
	return map[string]annotations.RelationSpec{
		"APC": {ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1,
			RecordTokens: 2, NameSection: "name"},
	}
}

func mergeRelations(base, extra map[string]annotations.RelationSpec) map[string]annotations.RelationSpec {
	out := make(map[string]annotations.RelationSpec, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

func readOptional(explicit string, fallback []byte) ([]byte, error) {
	if explicit == "" {
		return fallback, nil
	}
	data, err := os.ReadFile(explicit)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", explicit, err)
	}
	return data, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "edge-bench:", err)
	os.Exit(1)
}
