package extraction

import (
	"strings"
	"testing"

	"pvfine/internal/annotations"
)

// nestedFixture builds the smallest archive that can express a section index:
// a map whose count-prefixed record carries a `[hellparty]` block, an index
// entry whose groups list members, and two registries the members route into.
func nestedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixtureFrom(t, []entry{
		{"monster/monster.lst", "200\t`goblin/a.mob`\n"},
		{"monster/goblin/a.mob", "[name]\n\t`Goblin`\n"},
		{"aicharacter/aicharacter.lst", "300\t`hero/a.aic`\n"},
		{"aicharacter/hero/a.aic", "[name]\n\t`Hero`\n"},
		{"etc/group.etc", "[hellparty monster group]\n" +
			"[group index]\n\t7\n[group]\n\t`A`\t200\t0\t300\t1\n[/group]\n" +
			"[group index]\n\t8\n[group]\n\t`B`\t999\t0\t300\t5\n[/group]\n"},
		{"map/forest/hell.map", "[special passive object]\n" +
			"\t10\t1\t2\t3\t1\t`[hellparty]`\t7\t50\t1\t8\t50\t2\t9\t50\t3\t`[/hellparty]`\n"},
	}, nestedDocument())
	f.specs["怪物"] = annotations.RelationSpec{
		ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2}
	f.specs["APC"] = annotations.RelationSpec{
		ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1, RecordTokens: 2}
	return f
}

func nestedDocument() Document {
	return Document{Version: 1, Edges: []Edge{{
		ID: "map.specialPassiveObject", Extensions: []string{".map"},
		Section:    "special passive object",
		TokenTypes: []int32{0},
		Record: &Record{CountPrefixed: &CountPrefixed{
			PrefixTokens: 5, CountIndex: 4, ItemTokens: 6, KindIndex: 0,
			NestedKinds: []string{"hellparty"},
			NestedRefs: []NestedRef{{
				Kind: "hellparty", Width: 3, Offset: 0,
				Index: SectionIndex{
					Path: "etc/group.etc", IndexSection: "group index",
					MemberSection: "group", MemberWidth: 2, KindOffset: 1,
					Routes: []MemberRoute{{Kind: "0", Relation: "怪物"}, {Kind: "1", Relation: "APC"}},
				},
			}},
		}},
		Target: Target{Kind: TargetRegistry, Relation: "被动对象"},
	}}}
}

func TestSectionIndexFansOutThroughRegistries(t *testing.T) {
	f := nestedFixture(t)
	scanner := f.scannerWith(t, nestedDocument())
	result := f.scanFile(t, scanner, "map/forest/hell.map")

	byValue := map[string]Ref{}
	groupRefs := []Ref{}
	for _, ref := range result.Refs {
		if ref.Kind == TargetSectionIndex {
			groupRefs = append(groupRefs, ref)
			continue
		}
		byValue[ref.Relation+":"+ref.Value] = ref
	}

	// 记录首列仍是被动对象引用，段内索引引用另走一条路。
	if door, ok := byValue["被动对象:10"]; !ok || door.State != StateResolved {
		t.Fatalf("记录首列应解析为被动对象, got %+v", byValue)
	}
	if len(groupRefs) != 3 {
		t.Fatalf("三个三元组应产出 3 条段内索引引用, got %d %+v", len(groupRefs), groupRefs)
	}
	// 索引条目本身是依赖：命中的组必须把它作为可跟随目标带出来。
	resolvedGroups := 0
	for _, ref := range groupRefs {
		if ref.State != StateResolved {
			continue
		}
		resolvedGroups++
		if ref.TargetPath != "etc/group.etc" || ref.Target < 0 {
			t.Errorf("命中的组引用应指向索引条目, got %+v", ref)
		}
	}
	if resolvedGroups != 2 {
		t.Errorf("组 7、8 应命中，组 9 缺失, got %d", resolvedGroups)
	}
	want := []string{"怪物:200", "APC:300", "怪物:999"}
	for _, key := range want {
		if _, ok := byValue[key]; !ok {
			t.Errorf("成员引用 %s 缺失, got %v", key, keysOf(byValue))
		}
	}
	if ref := byValue["怪物:200"]; ref.State != StateResolved || ref.TargetPath != "monster/goblin/a.mob" {
		t.Errorf("kind=0 成员应解析到怪物表, got %+v", ref)
	}
	if ref := byValue["APC:300"]; ref.State != StateResolved || ref.TargetPath != "aicharacter/hero/a.aic" {
		t.Errorf("kind=1 成员应解析到 APC 表, got %+v", ref)
	}
	// 注册表里查不到的成员保持 missing，不能假装成功。
	if ref := byValue["怪物:999"]; ref.State != StateMissing {
		t.Errorf("未登记成员应为 missing, got %+v", ref)
	}
	// 未声明归属的 kind 是 unknown，绝不回退到"猜一张表"。
	unknownKind := 0
	missingGroup := 0
	for _, ref := range result.Refs {
		switch {
		case ref.State == StateUnknown && strings.Contains(ref.Note, "未声明归属"):
			unknownKind++
		case ref.State == StateMissing && strings.Contains(ref.Note, "索引里没有该组"):
			missingGroup++
		}
	}
	if unknownKind != 1 {
		t.Errorf("kind=5 成员应报告 1 条未声明归属, got %d", unknownKind)
	}
	if missingGroup != 1 {
		t.Errorf("组 9 不在索引里，应报告 1 条缺失, got %d", missingGroup)
	}

	// weight / wave 不是引用，但声明之后仍必须留在候选队列里，
	// 否则"补一条边"就顺带把别的东西变隐身了。
	shapes := map[string]int{}
	for _, candidate := range result.Unmodeled {
		shapes[candidate.Shape]++
	}
	if shapes["nested[hellparty]"] != 6 {
		t.Errorf("6 个非引用列应留在候选队列, got %v", shapes)
	}
}

func TestSectionIndexClosureIncludesIndexEntry(t *testing.T) {
	f := nestedFixture(t)
	scanner := f.scannerWith(t, nestedDocument())
	seed, ok := f.archive.Find("map/forest/hell.map")
	if !ok {
		t.Fatal("fixture 缺少地图")
	}
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	got := map[string]bool{}
	for _, node := range closure.Nodes {
		got[node.Path] = true
	}
	for _, want := range []string{"etc/group.etc", "monster/goblin/a.mob", "aicharacter/hero/a.aic"} {
		if !got[want] {
			t.Errorf("闭包缺少 %s（完整=%v）", want, closure.Paths())
		}
	}
}

func TestBuildSectionIndexReportsShapeDrift(t *testing.T) {
	f := nestedFixture(t)
	archive := f.archive
	spec := SectionIndex{Path: "etc/group.etc", IndexSection: "group index",
		MemberSection: "group", MemberWidth: 2, KindOffset: 1,
		Routes: []MemberRoute{{Kind: "0", Relation: "怪物"}}}
	idx := buildSectionIndex(archive, spec)
	if idx.absent {
		t.Fatal("索引条目应存在")
	}
	if len(idx.groups) != 2 {
		t.Fatalf("应读出 2 个组, got %d", len(idx.groups))
	}
	if got := idx.groups[7]; got.Tier != "A" || len(got.Members) != 2 {
		t.Errorf("组 7 应保留档位 A 与 2 个成员, got %+v", got)
	}
	if got := idx.groups[8]; got.Tier != "B" || got.Members[1].Kind != "5" {
		t.Errorf("组 8 的第 2 个成员 kind 应取第 2 列, got %+v", got.Members)
	}
	if idx.malformed != 0 {
		t.Errorf("对齐良好时 malformed 应为 0, got %d", idx.malformed)
	}

	// 成员宽度对不齐（版本改了列数）必须显式可见，而不是错位到下一列。
	drift := newFixtureFrom(t, []entry{
		{"etc/drift.etc", "[group index]\n\t1\n[group]\n\t`A`\t200\t0\t300\n[/group]\n"},
	}, nestedDocument())
	bad := buildSectionIndex(drift.archive, SectionIndex{Path: "etc/drift.etc",
		IndexSection: "group index", MemberSection: "group", MemberWidth: 2, KindOffset: 1})
	if bad.malformed != 1 {
		t.Errorf("成员流长度不能被宽度整除时应报告 malformed, got %d", bad.malformed)
	}
	if len(bad.groups[1].Members) != 1 {
		t.Errorf("只应收下完整的一条成员记录, got %+v", bad.groups[1].Members)
	}
}

func TestCatalogValidatesNestedRefs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*NestedRef, *CountPrefixed)
		want   string
	}{
		{"缺少 kind", func(n *NestedRef, _ *CountPrefixed) { n.Kind = "" }, "缺少 kind"},
		{"未登记配对块", func(_ *NestedRef, cp *CountPrefixed) { cp.NestedKinds = nil }, "未同时登记在 nestedKinds"},
		{"宽度非法", func(n *NestedRef, _ *CountPrefixed) { n.Width = 0 }, "width 必须为正"},
		{"列越界", func(n *NestedRef, _ *CountPrefixed) { n.Offset = 3 }, "必须落在"},
		{"索引缺路径", func(n *NestedRef, _ *CountPrefixed) { n.Index.Path = "" }, "缺少 path"},
		{"成员宽度不足", func(n *NestedRef, _ *CountPrefixed) { n.Index.MemberWidth = 1 }, "至少 2"},
		{"kind 列越界", func(n *NestedRef, _ *CountPrefixed) { n.Index.KindOffset = 2 }, "必须落在成员"},
		{"无路由", func(n *NestedRef, _ *CountPrefixed) { n.Index.Routes = nil }, "routes 为空"},
		{"路由缺 relation", func(n *NestedRef, _ *CountPrefixed) {
			n.Index.Routes = []MemberRoute{{Kind: "0"}}
		}, "缺少 kind 或 relation"},
	}
	for _, tc := range cases {
		doc := nestedDocument()
		cp := doc.Edges[0].Record.CountPrefixed
		target := &cp.NestedRefs[0]
		tc.mutate(target, cp)
		if _, err := NewCatalog(doc); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 期望错误含 %q, got %v", tc.name, tc.want, err)
		}
	}
}

func keysOf(m map[string]Ref) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
