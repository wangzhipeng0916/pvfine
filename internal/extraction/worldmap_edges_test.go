package extraction

import (
	"path/filepath"
	"strings"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// TestWorldmapEdgesFollowDeclaredColumns pins task #39. Three things can break
// here without any other test noticing:
//
//   - `[dungeon]` is (副本ID, -1) pairs, so only column 0 may be followed.
//   - that block may carry a NESTED `[in progress]` sub-block (powerstation.wdm
//     does) whose numbers are condition clues, not dungeon IDs — the knowledge
//     base says so explicitly, and a flat "every number in the section" edge
//     would pull in whole dungeons nobody listed.
//   - the direction is one way: `[map image]` is a client asset and a `.dgn`
//     never reaches back into a `.wdm`, which is what keeps the dungeon preset
//     at 223 entries instead of thousands.
func TestWorldmapEdgesFollowDeclaredColumns(t *testing.T) {
	dir := t.TempDir()
	created := pvf.New()
	add := func(p, text string) {
		t.Helper()
		if _, err := created.AddFileText(p, text, pvf.TypeScript); err != nil {
			t.Fatalf("AddFileText(%s): %v", p, err)
		}
	}
	add("worldmap/worldmap.lst", "1\t`worldmap/page.wdm`\n")
	add("dungeon/dungeon.lst", "100\t`dungeon/a.dgn`\n101\t`dungeon/b.dgn`\n999\t`dungeon/trap.dgn`\n")
	add("item/item.lst", "7\t`stk/ticket.stk`\n")
	add("dungeon/a.dgn", "[name]\n\t`A`\n[worldmap pattern info]\n\t`WorldMap/Pattern.img`\n")
	add("dungeon/b.dgn", "[name]\n\t`B`\n")
	add("dungeon/trap.dgn", "[name]\n\t`条件副本，不该被带出`\n")
	add("stk/ticket.stk", "[name]\n\t`门票`\n")
	add("worldmap/ui/page.xui", "[ui controls]\n\t1\n")
	// 形状取自 86 实测：map image 带一个帧号列，[dungeon] 里可以嵌 [in progress]。
	add("worldmap/page.wdm", "[map image]\n\t`WorldMap/Page.img`\t0\n\n"+
		"[ui path]\n\t`worldmap/ui/page.xui`\n\n"+
		"[dungeon]\n\t100\t-1\t101\t-1\n\t[in progress]\n\t\t999\t888\n[/dungeon]\n\n"+
		"[hell freepass item]\n\t7\t1\n[/hell freepass item]\n\n"+
		"[name]\n\t`洛兰`\n")

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
		"副本接口": {ListPath: "worldmap/worldmap.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"地下城":  {ListPath: "dungeon/dungeon.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"物品":   {ListPath: "item/item.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
	}
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载内置边目录失败: %v", err)
	}
	scanner := NewScanner(archive, catalog, NewRegistrySet(archive, specs))
	if err := scanner.Prewarm(nil); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}

	closureOf := func(t *testing.T, seed string) *Closure {
		t.Helper()
		index, ok := archive.Find(seed)
		if !ok {
			t.Fatalf("夹具里没有 %s", seed)
		}
		closure, err := scanner.Closure([]int32{index}, Options{})
		if err != nil {
			t.Fatalf("闭包 %s: %v", seed, err)
		}
		return closure
	}
	paths := func(closure *Closure) map[string]bool {
		got := map[string]bool{}
		for _, node := range closure.Nodes {
			got[strings.ToLower(node.Path)] = true
		}
		return got
	}

	page := closureOf(t, "worldmap/page.wdm")
	got := paths(page)
	for _, want := range []string{"dungeon/a.dgn", "dungeon/b.dgn", "worldmap/ui/page.xui", "stk/ticket.stk"} {
		if !got[want] {
			t.Errorf("[dungeon]/[ui path]/门票 应把 %s 带进闭包，实测集合 %v", want, keys(got))
		}
	}
	if got["dungeon/trap.dgn"] {
		t.Error("[in progress] 里的 999 被当成副本 ID 跟进了——知识库明令不得与普通副本列表合并抽数")
	}
	for _, asset := range []string{"worldmap/pattern.img", "worldmap/page.img"} {
		if got[asset] {
			t.Errorf("%s 是客户端资产，不该作为条目进闭包", asset)
		}
	}

	// 反方向必须走不到：这条断言撑着"副本预设 223 条目不受 #39 影响"的说法。
	if paths(closureOf(t, "dungeon/a.dgn"))["worldmap/page.wdm"] {
		t.Error(".dgn 反向走进了 .wdm，副本预设的闭包会从几百条膨胀到几千条")
	}

	// `.xui` 只有进了类别才有落点，否则世界地图包会往"其他"里堆 51 个文件。
	for _, node := range page.Nodes {
		if !strings.HasSuffix(strings.ToLower(node.Path), ".xui") {
			continue
		}
		if class := catalog.ClassifyNode(node); class.ID != "ui" {
			t.Errorf(".xui 应归入 ui 类别，实得 %s（%s）", class.ID, class.Reason)
		}
	}
}
