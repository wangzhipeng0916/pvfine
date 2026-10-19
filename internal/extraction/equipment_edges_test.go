package extraction

import (
	"path/filepath"
	"strings"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// TestEquipmentIDEdgesFollowDeclaredColumns pins the three shapes that task #34
// established for the equipment family, because each of them can silently break
// without any test failing:
//
//   - `[appendage]` resolves in the appendage table, whose 925 rows make a 1.000
//     hit rate meaningful (a big table would not be).
//   - `[summon monster]` is (ID, level, quantity): only column 0 may be followed.
//   - `[booster info]›[equipment]` repeats (ID, probability, quantity) but its
//     FIRST line is an item count, which shifts the flat token stream by one —
//     the reason the edge declares refOffsets [1]. Declare it as column 0 and the
//     gift box resolves nothing at all, which is exactly how a package would ship
//     without its contents.
func TestEquipmentIDEdgesFollowDeclaredColumns(t *testing.T) {
	dir := t.TempDir()
	created := pvf.New()
	add := func(p, text string) {
		t.Helper()
		if _, err := created.AddFileText(p, text, pvf.TypeScript); err != nil {
			t.Fatalf("AddFileText(%s): %v", p, err)
		}
	}
	add("appendage/appendage.lst", "5\t`appendage/extra.apd`\n")
	add("monster/monster.lst", "11\t`mob/mob.mob`\n")
	// 装备 与 物品(并表) 在这份夹具里指向同一批行，真实归档里 物品 是 装备+道具 的并。
	add("equipment/equipment.lst", "7\t`equ/target.equ`\n8\t`equ/other.equ`\n")
	add("item/item.lst", "7\t`equ/target.equ`\n8\t`equ/other.equ`\n3\t`stk/x.stk`\n")
	add("appendage/extra.apd", "[change attack info]\n\t1\n")
	add("mob/mob.mob", "[name]\n\t`怪`\n")
	add("equ/target.equ", "[name]\n\t`甲`\n")
	add("equ/other.equ", "[name]\n\t`乙`\n")
	add("stk/x.stk", "[name]\n\t`币`\n")
	add("equipment/a.equ", "[appendage]\n\t5\n[summon monster]\n\t11\n\t30\n\t2\n")
	add("stackable/box.stk", "[booster info]\n[equipment]\n\t2\n\t7\n\t1000\n\t1\n\t8\n\t1000\n\t1\n"+
		"[/equipment]\n[/booster info]\n")

	file := filepath.Join(dir, "Script.pvf")
	if err := created.SaveAs(file); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer archive.Release()

	specs := map[string]annotations.RelationSpec{
		"apd": {ListPath: "appendage/appendage.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"怪物":  {ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"装备":  {ListPath: "equipment/equipment.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"物品":  {ListPath: "item/item.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
	}
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载内置边目录失败: %v", err)
	}
	scanner := NewScanner(archive, catalog, NewRegistrySet(archive, specs))
	if err := scanner.Prewarm(nil); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}

	seed := func(t *testing.T, p string) map[string]bool {
		t.Helper()
		index, ok := archive.Find(p)
		if !ok {
			t.Fatalf("夹具缺少 %s", p)
		}
		closure, err := scanner.Closure([]int32{index}, Options{})
		if err != nil {
			t.Fatalf("Closure(%s): %v", p, err)
		}
		got := map[string]bool{}
		for _, node := range closure.Nodes {
			got[node.Path] = true
		}
		return got
	}

	fromEqu := seed(t, "equipment/a.equ")
	if !fromEqu["appendage/extra.apd"] {
		t.Errorf("[appendage] 的 ID 应解析到附件表指向的 .apd: %v", keys(fromEqu))
	}
	if !fromEqu["mob/mob.mob"] {
		t.Errorf("[summon monster] 首列应解析到怪物: %v", keys(fromEqu))
	}

	fromBox := seed(t, "stackable/box.stk")
	for _, want := range []string{"equ/target.equ", "equ/other.equ"} {
		if !fromBox[want] {
			t.Errorf("礼盒内容物 %s 没进闭包，说明首行条目数的错位没有被 refOffsets=[1] 吸收: %v", want, keys(fromBox))
		}
	}
	// 条目数 2 与概率/数量列 1000/1 都不是引用：它们若被当成 ID，闭包会多出内容物之外的文件。
	if len(fromBox) != 3 {
		t.Errorf("礼盒闭包应只有 .stk 自身 + 两条内容物, got %d: %v", len(fromBox), keys(fromBox))
	}
}

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// TestSectionTableEdgeResolvesOneRowOfATable pins the part-set semantics: the
// number in an .equ points at one block of etc/equipmentpartset.etc, whose
// second token is that set's `.equ`. Two properties matter and neither is
// obvious from the data: the table file itself must NOT be walked (following it
// pulls every set in the game into a package that belongs to exactly one), and a
// number the table does not define has to surface as a miss rather than nothing.
func TestSectionTableEdgeResolvesOneRowOfATable(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"etc/equipmentpartset.etc", "[equipment part set]\n\t2\n\t`character/partset/upperset.equ`\n\t`高级装扮 套装`\n" +
			"\t`帽子`\t`[hat avatar]`\t2\t0\n[/equipment part set]\n\n[equipment part set]\n\t3\n" +
			"\t`character/partset/rareset.equ`\n\t`稀有装扮 套装`\n[/equipment part set]\n"},
		{"equipment/character/partset/upperset.equ", "[name]\n\t`上衣套`\n"},
		{"equipment/character/partset/rareset.equ", "[name]\n\t`稀有套`\n"},
		{"equipment/skin.equ", "[part set index]\n\t2\n"},
		{"equipment/unknown.equ", "[part set index]\n\t77\n"},
	}, testDocument())
	defer f.archive.Release()

	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载内置边目录失败: %v", err)
	}
	scanner := NewScanner(f.archive, catalog, f.registrySet())

	seed := func(t *testing.T, p string) map[string]bool {
		t.Helper()
		index, ok := f.archive.Find(p)
		if !ok {
			t.Fatalf("夹具缺少 %s", p)
		}
		closure, err := scanner.Closure([]int32{index}, Options{})
		if err != nil {
			t.Fatalf("Closure(%s): %v", p, err)
		}
		got := map[string]bool{}
		for _, node := range closure.Nodes {
			got[node.Path] = true
		}
		return got
	}

	fromSkin := seed(t, "equipment/skin.equ")
	if !fromSkin["equipment/character/partset/upperset.equ"] {
		t.Errorf("编号 2 应解出它自己那一套 .equ: %v", keys(fromSkin))
	}
	if fromSkin["equipment/character/partset/rareset.equ"] {
		t.Errorf("别套的 .equ 不该被拉进来: %v", keys(fromSkin))
	}
	if fromSkin["etc/equipmentpartset.etc"] {
		t.Error("表文件本身不得跟随：展开它等于把整张表的套装都塞进包")
	}
	result, err := scanner.ScanFile(mustIndex(t, f.archive, "equipment/unknown.equ"))
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	missed := false
	for _, ref := range result.Refs {
		if ref.EdgeID == "equ.partSetIndex" && ref.State == StateMissing {
			missed = true
			if !strings.Contains(ref.Note, "表里没有该编号") {
				t.Errorf("缺失要说明是编号不在表里: %q", ref.Note)
			}
		}
	}
	if !missed {
		t.Error("未知编号必须报缺失，不能静默返回空闭包")
	}
}

func mustIndex(t *testing.T, archive *pvf.Archive, p string) int32 {
	t.Helper()
	index, ok := archive.Find(p)
	if !ok {
		t.Fatalf("夹具缺少 %s", p)
	}
	return index
}
