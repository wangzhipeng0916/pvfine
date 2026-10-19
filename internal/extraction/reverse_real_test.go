package extraction_test

import (
	"context"
	"path"
	"runtime"
	"strings"
	"testing"
	"time"

	"pvfine/internal/extraction"
)

// TestReverseIndexHoldsOnRealArchive is E11/E12/E14's standing guard on real
// data: the reverse view must agree with the forward view everywhere, registered
// content must never be reported as dead, and the hellparty chain built in #21
// must be reachable backwards.
func TestReverseIndexHoldsOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	if err := scanner.Prewarm(nil); err != nil {
		t.Fatalf("预热失败: %v", err)
	}
	indexes := make([]int32, 0, archive.FileCount())
	for index := int32(0); index < archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	started := time.Now()
	reverse, err := extraction.BuildReverseIndex(context.Background(), scanner, indexes, runtime.NumCPU())
	if err != nil {
		t.Fatalf("建反向索引失败: %v", err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("反向索引：扫描=%d 原始链接=%d 去重后=%d 覆盖条目=%d 建索引=%s 估算=%.0f MB (堆此刻 %.0f MB)",
		reverse.Universe, reverse.RawLinks, reverse.Refs, reverse.Entries,
		time.Since(started).Round(time.Millisecond),
		float64(reverse.BytesEstimate())/1e6, float64(mem.HeapAlloc)/1e6)
	if reverse.Refs == 0 || reverse.Refs > reverse.RawLinks {
		t.Fatalf("链接数不合理: refs=%d raw=%d", reverse.Refs, reverse.RawLinks)
	}

	seeds := []string{"dungeon/act1/grakkarak.dgn",
		"map/190_watchtower/hell_38142(3,0)normal.map",
		"map/amon/hell_amonhigh.map"}
	for _, seedPath := range seeds {
		seed, ok := archive.Find(seedPath)
		if !ok {
			t.Logf("归档不含 %s，跳过", seedPath)
			continue
		}
		closure, err := scanner.Closure([]int32{seed}, extraction.Options{})
		if err != nil {
			t.Fatalf("闭包失败 %s: %v", seedPath, err)
		}
		inClosure := map[int32]bool{}
		for _, node := range closure.Nodes {
			inClosure[node.Index] = true
		}
		// 正向 ⊆ 反向：闭包认定的每条边，入边表都得认。
		for _, link := range closure.Edges {
			found := false
			for _, incoming := range reverse.Incoming(link.To) {
				if incoming.From == link.From && reverse.EdgeName(incoming.Name) == link.Ref.EdgeID {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s: 闭包边 %s → %s (%s) 不在反向索引里", seedPath,
					archive.FullPath(link.From), archive.FullPath(link.To), link.Ref.EdgeID)
			}
		}
		// 反向 ⊆ 正向：入边表里来自闭包内条目的边级链接，目标必须也在闭包内。
		for _, node := range closure.Nodes {
			for _, incoming := range reverse.Incoming(node.Index) {
				if incoming.Kind != extraction.IncomingEdge || incoming.From < 0 {
					continue
				}
				if !inClosure[incoming.From] {
					continue // 来自闭包外的引用与本断言无关
				}
				probe, err := scanner.Closure([]int32{incoming.From}, extraction.Options{})
				if err != nil {
					t.Fatalf("闭包失败 %s: %v", archive.FullPath(incoming.From), err)
				}
				found := false
				for _, reached := range probe.Nodes {
					if reached.Index == node.Index {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("反向边 %s → %s 在正向闭包里找不到",
						archive.FullPath(incoming.From), archive.FullPath(node.Index))
				}
			}
		}
		t.Logf("%s：闭包=%d 条目，反向对偶成立", path.Base(seedPath), len(closure.Nodes))
	}

	// #21 的段内索引链必须能反向走通：深渊图把索引表拉进来。
	if etc, ok := archive.Find("etc/hellparty.etc"); ok {
		links := reverse.Incoming(etc)
		if len(links) == 0 {
			t.Error("etc/hellparty.etc 没有任何入边，说明深渊组链在反向上是断的")
		}
		hell := 0
		for _, link := range links {
			if link.From >= 0 && strings.Contains(strings.ToLower(path.Base(archive.FullPath(link.From))), "hell") {
				hell++
			}
		}
		depths := map[int]int{}
		for _, hit := range reverse.Ancestors(etc, 0) {
			depths[hit.Depth]++
		}
		t.Logf("etc/hellparty.etc：入边=%d（深渊图=%d）传递祖先按跳数=%v", len(links), hell, depths)
		if hell == 0 {
			t.Error("入边里没有深渊图，#21 的反向可用性不成立")
		}
	}

	// E14 的安全边界：登记过的内容绝不能被判成死内容。
	orphans, rows := reverse.Orphans()
	registers := scanner.RegistrySet()
	registered := map[string]bool{}
	for _, relation := range registers.Relations() {
		index, err := registers.PathIndex(relation)
		if err != nil {
			continue
		}
		for registeredPath := range index {
			registered[strings.ToLower(registeredPath)] = true
		}
	}
	violations := 0
	samples := []string{}
	for _, index := range orphans {
		full := strings.ReplaceAll(strings.ToLower(archive.FullPath(index)), "\\", "/")
		base := strings.TrimPrefix(full, "/")
		if registered[base] || registered[strings.TrimPrefix(base, "./")] {
			violations++
			if len(samples) < 5 {
				samples = append(samples, archive.FullPath(index))
			}
		}
	}
	if violations > 0 {
		t.Errorf("有 %d 个登记条目被判成孤儿（样例 %v）", violations, samples)
	}
	summarised := 0
	for _, row := range rows {
		summarised += row.Count
	}
	if summarised != len(orphans) {
		t.Errorf("孤儿排行合计 %d 与实际 %d 不符", summarised, len(orphans))
	}
	t.Logf("孤儿（既无入边也无登记）=%d 条目，占全档 %.1f%%；前几类 %v",
		len(orphans), 100*float64(len(orphans))/float64(archive.FileCount()), rows[:minRows(6, len(rows))])
}

func minRows(a, b int) int {
	if a < b {
		return a
	}
	return b
}
