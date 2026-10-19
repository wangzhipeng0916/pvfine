package extraction

import (
	"strings"
	"testing"

	"pvfine/internal/annotations"
)

// TestUnionRegistryKeepsMemberDirectories pins the bug that the drop edges
// exposed: a union relation merges its members' id→path rows, but each member
// row is relative to *its own* .lst directory. Dropping that directory made
// every union lookup fall back to the archive root, so 物品 (装备+道具, measured
// 162,567 rows on 86) resolved nothing at all — a total-loss bug invisible in
// every earlier test because none of them declared a union relation.
func TestUnionRegistryKeepsMemberDirectories(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{
		{ID: "mob.dropItem", Extensions: []string{".mob"}, Section: "item",
			TokenTypes: []int32{0}, Record: &Record{Width: 2, RefOffsets: []int{0}},
			Target: Target{Kind: TargetRegistry, Relation: "物品"}},
	}}
	f := newFixtureFrom(t, []entry{
		{"gear/gear.lst", "700\t`hat.equ`\n"},
		{"consum/consum.lst", "701\t`potion.stk`\n"},
		{"gear/hat.equ", "[name]\n\t`Hat`\n"},
		{"consum/potion.stk", "[name]\n\t`Potion`\n"},
		{"monster/goblin/union.mob", "[item]\n\t700\t50\t701\t200\n"},
	}, doc)
	f.specs["装备"] = annotations.RelationSpec{
		ListPath: "gear/gear.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	f.specs["道具"] = annotations.RelationSpec{
		ListPath: "consum/consum.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	f.specs["物品"] = annotations.RelationSpec{
		Kind: "union", Relations: []string{"装备", "道具"},
	}
	scanner := f.scannerWith(t, doc)

	refs := f.scanNode(t, scanner, "monster/goblin/union.mob")
	if len(refs) != 2 {
		t.Fatalf("2 列宽的掉落记录应命中 2 个物品 ID, got %d: %+v", len(refs), refs)
	}
	want := map[string]string{"700": "gear/hat.equ", "701": "consum/potion.stk"}
	for _, ref := range refs {
		expect, ok := want[ref.Value]
		if !ok {
			t.Fatalf("意外的掉落 ID %q", ref.Value)
		}
		if ref.State != StateResolved {
			t.Errorf("掉落 %s 应经 union 解析到 %s, got %s (%s)", ref.Value, expect, ref.State, ref.Note)
			continue
		}
		if ref.TargetPath != expect {
			t.Errorf("掉落 %s 目标应为 %s, got %s", ref.Value, expect, ref.TargetPath)
		}
	}
}

// TestDropRecordKeepsColumnParity proves the drop edges need no line grouping:
// observed 86 `[item]` rows are always an even number of fields (`ID prob`
// pairs), so the flat column-0 rule lands exactly on the identifiers and never
// on a probability.
func TestDropRecordKeepsColumnParity(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{
		{ID: "mob.dropItem", Extensions: []string{".mob"}, Section: "item",
			TokenTypes: []int32{0}, Record: &Record{Width: 2, RefOffsets: []int{0}},
			Target: Target{Kind: TargetRegistry, Relation: "物品"}},
	}}
	f := newFixtureFrom(t, []entry{
		{"consum/consum.lst", "701\t`potion.stk`\n702\t`stone.stk`\n"},
		{"consum/potion.stk", "[name]\n\t`Potion`\n"},
		{"consum/stone.stk", "[name]\n\t`Stone`\n"},
		// 86 实测形状：goblin.mob 为 `1000 50 1047 200 1004 100`，三对。
		{"monster/goblin/pairs.mob", "[item]\n\t701\t50\t702\t200\n"},
	}, doc)
	f.specs["物品"] = annotations.RelationSpec{
		ListPath: "consum/consum.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	scanner := f.scannerWith(t, doc)

	refs := f.scanNode(t, scanner, "monster/goblin/pairs.mob")
	if len(refs) != 2 {
		t.Fatalf("期望 2 条掉落引用（概率列不得计入）, got %d: %+v", len(refs), refs)
	}
	for _, ref := range refs {
		if ref.Value != "701" && ref.Value != "702" {
			t.Errorf("概率列 %q 被误当作物品 ID", ref.Value)
		}
	}
}

// TestRegistryHandlesBothListLayouts pins the two things the 110 archive taught
// us (task #40/#41), each of which failed silently in a different way:
//
//   - 86/90 keep a table next to its content (`dungeon/dungeon.lst`) and write
//     rows relative to that directory (`act1/lorien.dgn`). paged110 keeps every
//     table under `list/` and writes rows that already carry the directory
//     (`dungeon/act1/lorien.dgn`).
//   - `PathIndex` used to answer "is this file registered?" by joining the
//     list's own directory onto every row, which on the 110 shape produced
//     `dungeon/dungeon/act1/...` for every single row — and it feeds 反向可达性、
//     孤儿检测与登记建议, so the whole answer set was wrong, not one lookup.
func TestRegistryHandlesBothListLayouts(t *testing.T) {
	cases := []struct {
		name     string
		listPath string
		row      string
	}{
		{"86 形：表在内容目录、行相对", "dungeon/dungeon.lst", "act1/lorien.dgn"},
		{"110 形：表在 list/、行带目录", "list/dungeon.lst", "dungeon/act1/lorien.dgn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureFrom(t, []entry{
				{tc.listPath, "1\t`" + tc.row + "`\n"},
				{"dungeon/act1/lorien.dgn", "[name]\n\t`洛兰`\n"},
			}, Document{Version: 1})
			// 声明路径统一按 config/lists.json 的写法给出，两种归档都必须由
			// FindList 自己找到实际位置；否则 110 上整个 registry 侧静默全灭
			// （实测 18 张表 0 张能建，missing 320 万，副本闭包 223 → 3 条目）。
			f.specs["地下城"] = annotations.RelationSpec{
				ListPath: "dungeon/dungeon.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
			}
			registers := f.registrySet()
			target, err := registers.Resolve("地下城", "1")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if target.Path != "dungeon/act1/lorien.dgn" {
				t.Errorf("行 %q 解析到 %s", tc.row, target.Path)
			}
			index, err := registers.PathIndex("地下城")
			if err != nil {
				t.Fatalf("PathIndex: %v", err)
			}
			if id, ok := index["dungeon/act1/lorien.dgn"]; !ok || id != "1" {
				t.Errorf("登记表路径要给真实归档路径，实得 %v", index)
			}
			for registered := range index {
				if strings.HasPrefix(registered, "dungeon/dungeon/") {
					t.Errorf("目录被拼了两次：%s", registered)
				}
			}
		})
	}
}

// TestUnionRegistryCanonicalizesMemberRows covers the same defect in the union
// branch, where it is worse: a union lookup never goes back through locate, so a
// doubled directory kills every row at once. 110's 物品 (装备+道具) measured
// 555,759 rows all shaped `stackable/stackable/...`, which is why `mob.dropItem`
// resolved 0 of 1,196 values there. This fixture deliberately mixes both layouts
// so a fix that only serves one of them cannot pass.
func TestUnionRegistryCanonicalizesMemberRows(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"gear/gear.lst", "700\t`hat.equ`\n"},               // 86 形：行相对表目录
		{"consum/consum.lst", "701\t`consum/potion.stk`\n"}, // 110 形：行自带目录
		{"gear/hat.equ", "[name]\n\t`Hat`\n"},
		{"consum/potion.stk", "[name]\n\t`Potion`\n"},
	}, Document{Version: 1})
	f.specs["装备"] = annotations.RelationSpec{
		ListPath: "gear/gear.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	f.specs["道具"] = annotations.RelationSpec{
		ListPath: "consum/consum.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	f.specs["物品"] = annotations.RelationSpec{
		Kind: "union", Relations: []string{"装备", "道具"},
	}
	registers := f.registrySet()
	for id, want := range map[string]string{"700": "gear/hat.equ", "701": "consum/potion.stk"} {
		target, err := registers.Resolve("物品", id)
		if err != nil {
			t.Fatalf("Resolve(物品:%s): %v", id, err)
		}
		if target.Path != want {
			t.Errorf("物品:%s 解析到 %s，应为 %s", id, target.Path, want)
		}
	}
	index, err := registers.PathIndex("物品")
	if err != nil {
		t.Fatalf("PathIndex(物品): %v", err)
	}
	if len(index) != 2 {
		t.Fatalf("并表应有 2 行, got %d: %v", len(index), index)
	}
	for registered := range index {
		if strings.HasPrefix(registered, "gear/gear/") || strings.HasPrefix(registered, "consum/consum/") {
			t.Errorf("并表成员目录被拼了两次：%s", registered)
		}
	}
}
