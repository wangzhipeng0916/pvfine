package extraction

import (
	"path"
	"strings"
	"testing"
)

// TestSectionIndexHoldsOnRealArchive is the standing guard for the 段内索引
// declaration. It checks three separate things, because a section index can fail
// in three different ways: the table stops parsing (a revision changed the
// shape), the fan-out stops resolving (a registry moved), or the closure stops
// containing what the index implies.
func TestSectionIndexHoldsOnRealArchive(t *testing.T) {
	archive, scanner := testArchiveWithRelations(t)
	catalog := scanner.Catalog()

	type declared struct {
		edgeID string
		ref    NestedRef
	}
	var specs []declared
	for _, edge := range catalog.Document().Edges {
		cp := edge.Record.countPrefixed()
		if cp == nil {
			continue
		}
		for _, ref := range cp.NestedRefs {
			specs = append(specs, declared{edgeID: edge.ID, ref: ref})
		}
	}
	if len(specs) == 0 {
		t.Fatal("边目录未声明任何段内索引引用")
	}
	hostEdge := specs[0].edgeID

	// 1) 表必须能解析，且成员流宽度整齐。
	for _, spec := range specs {
		if _, ok := archive.Find(spec.ref.Index.Path); !ok {
			t.Skipf("归档不含 %s，本版本无此索引", spec.ref.Index.Path)
		}
		idx := scanner.sectionIndexFor(spec.ref.Index)
		members := 0
		for _, group := range idx.groups {
			members += len(group.Members)
		}
		t.Logf("%s: 键=%d 有成员组=%d 成员=%d 宽度漂移=%d",
			spec.ref.Index.Path, len(idx.groups), idx.records, members, idx.malformed)
		if idx.absent {
			t.Fatalf("%s 解析失败", spec.ref.Index.Path)
		}
		if idx.malformed != 0 {
			t.Errorf("%s 有 %d 组成员流宽度对不上，声明需要复核", spec.ref.Index.Path, idx.malformed)
		}
		if idx.records == 0 {
			t.Errorf("%s 一个组成员都没解析出来，声明可能已失效", spec.ref.Index.Path)
		}
	}

	// 2) 全库扫描：组号解析率与成员路由命中率必须站在对照列同一档。
	//    对照 = 记录首列（已知的 被动对象 引用），它给出真实引用能达到的上限。
	controlHits, controlTotal := 0, 0
	groupHits, groupTotal, groupMissing := 0, 0, 0
	memberHits, memberTotal, memberMissing, memberUnknownKind := 0, 0, 0, 0
	mapsWithBlock := 0
	seen := map[string]bool{}
	for index := int32(0); index < archive.FileCount(); index++ {
		entryPath := archive.FullPath(index)
		if !strings.EqualFold(path.Ext(entryPath), ".map") {
			continue
		}
		result, err := scanner.ScanFile(index)
		if err != nil {
			continue
		}
		fileHadGroup := false
		for _, ref := range result.Refs {
			switch {
			case ref.EdgeID == hostEdge && ref.Kind == TargetRegistry && ref.Relation == "被动对象":
				controlTotal++
				if ref.State == StateResolved {
					controlHits++
				}
			case ref.Kind == TargetSectionIndex:
				groupTotal++
				fileHadGroup = true
				if ref.State == StateResolved {
					groupHits++
				} else if ref.State == StateMissing {
					groupMissing++
				}
			case ref.Kind == TargetRegistry && ref.Section == "special passive object" &&
				ref.Relation != "" && ref.Relation != "被动对象":
				// 段的组引用之后紧跟的成员引用：relation 只会是路由表里的表。
				memberTotal++
				if ref.State == StateResolved {
					memberHits++
				} else if ref.State == StateMissing {
					memberMissing++
				}
			case ref.State == StateUnknown && strings.Contains(ref.Note, "未声明归属"):
				memberUnknownKind++
			}
		}
		if fileHadGroup {
			mapsWithBlock++
			seen[entryPath] = true
		}
	}
	if groupTotal == 0 {
		t.Skip("本归档没有段内索引引用可核验")
	}
	control := float64(controlHits) / float64(maxInt(controlTotal, 1))
	groupRate := float64(groupHits) / float64(groupTotal)
	memberRate := float64(memberHits) / float64(maxInt(memberTotal, 1))
	t.Logf("含块的 .map=%d；对照列命中=%.4f (%d/%d)；组号命中=%.4f (%d/%d，索引缺失 %d)；成员命中=%.4f (%d/%d，未登记 %d)；未知 kind=%d",
		mapsWithBlock, control, controlHits, controlTotal, groupRate, groupHits, groupTotal, groupMissing,
		memberRate, memberHits, memberTotal, memberMissing, memberUnknownKind)
	if control < 0.99 {
		t.Fatalf("对照列命中率仅 %.4f，本守卫无法判断段内索引是否可用", control)
	}
	if groupRate < 0.90 {
		t.Errorf("组号解析率 %.4f 低于 0.90，索引结构可能已变（缺失 %d 条）", groupRate, groupMissing)
	}
	if memberRate < 0.95 {
		t.Errorf("成员 registry 命中率 %.4f 低于 0.95（未登记 %d 条）", memberRate, memberMissing)
	}

	// 3) 功能证明：一张深渊图的闭包必须真的带上索引条目与怪物。
	var sample string
	for entryPath := range seen {
		if strings.Contains(strings.ToLower(path.Base(entryPath)), "hell") {
			sample = entryPath
			break
		}
	}
	if sample == "" {
		for entryPath := range seen {
			sample = entryPath
			break
		}
	}
	seed, ok := archive.Find(sample)
	if !ok {
		t.Fatalf("种子 %s 查不到", sample)
	}
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	gotIndex, gotMonster := false, false
	for _, node := range closure.Nodes {
		switch strings.ToLower(path.Ext(node.Path)) {
		case ".etc":
			gotIndex = true
		case ".mob", ".aic":
			gotMonster = true
		}
	}
	t.Logf("样本 %s：闭包=%d 条目，含索引条目=%v 含怪物/AI=%v",
		path.Base(sample), len(closure.Nodes), gotIndex, gotMonster)
	if !gotIndex || !gotMonster {
		t.Errorf("闭包没有穿过段内索引拿到怪物/AI，这条链实际没接通")
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
