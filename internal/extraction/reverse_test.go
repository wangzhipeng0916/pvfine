package extraction

import (
	"context"
	"strings"
	"testing"
)

func reverseFixture(t *testing.T) (*fixture, *Scanner, *ReverseIndex) {
	t.Helper()
	f := newFixture(t)
	scanner := f.scanner(t)
	indexes := make([]int32, 0, f.archive.FileCount())
	for index := int32(0); index < f.archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	reverse, err := BuildReverseIndex(context.Background(), scanner, indexes, 1)
	if err != nil {
		t.Fatalf("BuildReverseIndex: %v", err)
	}
	return f, scanner, reverse
}

func hasIncoming(r *ReverseIndex, to, from int32, edgeName string) bool {
	for _, link := range r.Incoming(to) {
		if link.From == from && r.EdgeName(link.Name) == edgeName {
			return true
		}
	}
	return false
}

// TestReverseIndexIsDualToClosure is the correctness bar for the whole feature:
// the reverse index must say exactly what the forward walk says. Any drift means
// one of the two views is silently dropping links.
func TestReverseIndexIsDualToClosure(t *testing.T) {
	f, scanner, reverse := reverseFixture(t)

	// 正向 ⊆ 反向：闭包里每条边都必须在入边表里出现。
	// 反向 ⊆ 正向：入边表里每条边级链接，其来源的闭包必须含目标。
	for seed := int32(0); seed < f.archive.FileCount(); seed++ {
		closure, err := scanner.Closure([]int32{seed}, Options{CollectUnmodeled: false})
		if err != nil {
			t.Fatalf("Closure(%s): %v", f.archive.FullPath(seed), err)
		}
		inClosure := map[int32]bool{}
		for _, node := range closure.Nodes {
			inClosure[node.Index] = true
		}
		for _, link := range closure.Edges {
			if !hasIncoming(reverse, link.To, link.From, link.Ref.EdgeID) {
				t.Fatalf("闭包边 %s → %s (%s) 不在反向索引里",
					f.archive.FullPath(link.From), f.archive.FullPath(link.To), link.Ref.EdgeID)
			}
		}
		for index := range inClosure {
			for _, link := range reverse.Incoming(index) {
				if link.Kind != IncomingEdge || link.From < 0 {
					continue
				}
				probe, err := scanner.Closure([]int32{link.From}, Options{})
				if err != nil {
					t.Fatalf("Closure(%s): %v", f.archive.FullPath(link.From), err)
				}
				found := false
				for _, node := range probe.Nodes {
					if node.Index == index {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("反向边 %s → %s 在正向闭包里找不到",
						f.archive.FullPath(link.From), f.archive.FullPath(index))
				}
			}
		}
	}
	if reverse.RawLinks < reverse.Refs {
		t.Errorf("去重后链接数不可能少于原始数: raw=%d refs=%d", reverse.RawLinks, reverse.Refs)
	}
}

// TestReverseAncestorsMatchForwardReachability checks the transitive statement a
// user actually asks: "if I change this file, which of my seeds are affected".
func TestReverseAncestorsMatchForwardReachability(t *testing.T) {
	f, scanner, reverse := reverseFixture(t)
	target, ok := f.archive.Find("passiveobject/obj/door.ani")
	if !ok {
		t.Fatal("fixture 缺少 door.ani")
	}
	hits := reverse.Ancestors(target, 0)
	byIndex := map[int32]int{}
	for _, hit := range hits {
		byIndex[hit.Index] = hit.Depth
	}
	for _, path := range []string{"map/forest/a.map", "passiveobject/obj/door.obj"} {
		index, ok := f.archive.Find(path)
		if !ok {
			t.Fatalf("fixture 缺少 %s", path)
		}
		closure, err := scanner.Closure([]int32{index}, Options{})
		if err != nil {
			t.Fatalf("Closure(%s): %v", path, err)
		}
		reachable := false
		for _, node := range closure.Nodes {
			if node.Index == target {
				reachable = true
			}
		}
		depth, inReverse := byIndex[index]
		if reachable != inReverse {
			t.Fatalf("%s: 正向可达=%v 反向祖先含它=%v（正向 ⊄ 反向，两套视图不一致）", path, reachable, inReverse)
		}
		if inReverse && depth < 1 {
			t.Errorf("%s 的反向深度应 ≥1, got %d", path, depth)
		}
	}
	// 深度语义：直接引用者深度 1，副本种子隔一跳。
	if door, ok := f.archive.Find("passiveobject/obj/door.obj"); ok && byIndex[door] != 1 {
		t.Errorf("door.obj 直接引用 door.ani，深度应为 1, got %d", byIndex[door])
	}
	if aMap, ok := f.archive.Find("map/forest/a.map"); ok && byIndex[aMap] != 2 {
		t.Errorf("a.map 经 door.obj 到 door.ani，深度应为 2, got %d", byIndex[aMap])
	}
}

func TestReverseIndexImpactIntersectsWithSeeds(t *testing.T) {
	f, _, reverse := reverseFixture(t)
	changed, ok := f.archive.Find("passiveobject/obj/door.ani")
	if !ok {
		t.Fatal("fixture 缺少 door.ani")
	}
	aMap, _ := f.archive.Find("map/forest/a.map")
	bMap, _ := f.archive.Find("map/forest/b.map")
	til, _ := f.archive.Find("map/forest/tiles/ground.til")
	touched := reverse.Impact([]int32{changed}, []int32{aMap, bMap, til})[changed]
	// a.map 直接引用 door.obj；b.map 也通过 passive object 10 走到它；
	// ground.til 是它们的下游而不是上游，改动不波及。
	if len(touched) != 2 || !hasIndex(touched, aMap) || !hasIndex(touched, bMap) {
		t.Fatalf("改动 door.ani 应波及 a.map 与 b.map, got %v", touched)
	}
	if hasIndex(touched, til) {
		t.Errorf("ground.til 是被引用方，不该出现在影响面里")
	}
	// 孤儿没有任何入边，改动它不波及任何人。
	hidden, _ := f.archive.Find("passiveobject/obj/hidden.ani")
	if got := reverse.Impact([]int32{hidden}, []int32{aMap, bMap})[hidden]; len(got) != 0 {
		t.Errorf("hidden.ani 无人引用，影响面应为空, got %v", got)
	}
}

func hasIndex(list []int32, want int32) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestReverseIndexOrphansExcludesRegisteredAndLists guards E14's definition:
// dead content is what neither a resolved edge nor a registry row names. Lists
// themselves are loaded by name, so calling them orphans is a false alarm.
func TestReverseIndexOrphansExcludesRegisteredAndLists(t *testing.T) {
	f, _, reverse := reverseFixture(t)
	orphans, rows := reverse.Orphans()
	names := []string{}
	for _, index := range orphans {
		names = append(names, f.archive.FullPath(index))
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "passiveobject/obj/hidden.ani") {
		t.Errorf("hidden.ani 只被未声明的 [mystery anim] 提到，应判为孤儿, got %v", names)
	}
	for _, mustNotBeOrphan := range []string{
		"map/map.lst",                     // 登记表本体
		"passiveobject/passiveobject.lst", // 登记表本体
		"map/forest/b.map",                // 无人引用，但登记在 map.lst
		"passiveobject/obj/crate.obj",     // 登记在 passiveobject.lst
		"passiveobject/obj/door.obj",      // 被 a.map 的段引用
		"passiveobject/obj/door.ani",      // 被 door.obj 的 [use animation] 引用
		"map/forest/tiles/ground.til",     // 被 a.map 的 [tile] 引用
	} {
		if strings.Contains(joined, mustNotBeOrphan) {
			t.Errorf("%s 不该算孤儿", mustNotBeOrphan)
		}
	}
	if len(rows) == 0 {
		t.Error("孤儿排行不应为空")
	}
}
