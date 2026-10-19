package extraction

import (
	"os"
	"path/filepath"
	"testing"

	"pvfine/internal/annotations"
)

// TestSuggestRegistrationsFindsUnlistedReferencedEntries is the E7 contract:
// an entry that something inside the set references, whose extension belongs to
// a registry, and which that registry does not list, must be proposed. Entries
// reached through the registry are registered by construction and must not be
// proposed, and files whose extension no registry claims must be left alone.
func TestSuggestRegistrationsFindsUnlistedReferencedEntries(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{
		// 种子通过路径直接引用一个 .obj，而不是通过 registry。
		{ID: "map.partnerObject", Extensions: []string{".map"}, Section: "partner object",
			TokenTypes: []int32{6}, Target: Target{Kind: TargetEntry, ExpectExt: []string{".obj"}}},
		// 通过 registry 引用的那条链，用于证明"已登记的不提"。
		{ID: "map.passiveObject", Extensions: []string{".map"}, Section: "passive object",
			TokenTypes: []int32{0}, Record: &Record{Width: 4, RefOffsets: []int{0}},
			Target: Target{Kind: TargetRegistry, Relation: "被动对象"}},
	}}
	f := newFixtureFrom(t, []entry{
		// 裸文件名：D1 的 basename 兜底会把 obj/unlisted.obj 解析到
		// passiveobject/obj/unlisted.obj，而它并不在 passiveobject.lst 里。
		{"map/forest/links.map", "[partner object]\n\t`unlisted.obj`\n" +
			"[passive object]\n\t10\n\t1\n\t2\n\t3\n"},
		{"passiveobject/obj/unlisted.obj", "[use animation]\n\t`door.ani`\n"},
	}, doc)
	f.specs["被动对象"] = annotations.RelationSpec{
		ListPath: "passiveobject/passiveobject.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	scanner := f.scannerWith(t, doc)
	seed, ok := f.archive.Find("map/forest/links.map")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	exported := make([]int32, 0, len(closure.Nodes))
	for _, node := range closure.Nodes {
		exported = append(exported, node.Index)
	}
	suggestions, err := SuggestRegistrations(scanner.RegistrySet(), closure, exported)
	if err != nil {
		t.Fatalf("SuggestRegistrations: %v", err)
	}
	if len(suggestions) != 1 {
		t.Fatalf("应恰好提出 1 条建议, got %d: %+v", len(suggestions), suggestions)
	}
	got := suggestions[0]
	if got.EntryPath != "passiveobject/obj/unlisted.obj" {
		t.Errorf("建议目标错误: %s", got.EntryPath)
	}
	if got.Relation != "被动对象" || got.ListPath != "passiveobject/passiveobject.lst" {
		t.Errorf("建议未指向正确的 registry: %+v", got)
	}
	if got.ReferencedBy != "map/forest/links.map" || got.EdgeID != "map.partnerObject" {
		t.Errorf("建议没写清引用来源: %+v", got)
	}
	for _, item := range suggestions {
		if filepath.Base(item.EntryPath) == "door.obj" {
			t.Error("已登记的条目不得出现在建议里")
		}
	}

	// 不请求建议时，Export 不得产生任何建议（默认关）。
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	quietDir := t.TempDir()
	result, err := Export(f.archive, closure, ExportOptions{Dir: quietDir, WithReports: true, Plan: plan})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("默认不应产出登记建议, got %d", len(result.Suggestions))
	}
	if _, err := os.Stat(filepath.Join(quietDir, "report", RegistrationFile)); err == nil {
		t.Error("未开启时不应写出 (5)登记建议.txt")
	}

	// 请求建议时，报告与 manifest 都要带上。
	loudDir := t.TempDir()
	loud, err := Export(f.archive, closure, ExportOptions{
		Dir: loudDir, WithReports: true, Plan: plan,
		LstSuggest: true, Scanner: scanner,
	})
	if err != nil {
		t.Fatalf("Export(suggest): %v", err)
	}
	if len(loud.Suggestions) != 1 {
		t.Errorf("开启后应有 1 条建议, got %d", len(loud.Suggestions))
	}
	if loud.Manifest.Selection == nil || len(loud.Manifest.Selection.LstSuggestions) != 1 {
		t.Errorf("manifest 未记录登记建议")
	}
	if _, err := os.Stat(filepath.Join(loudDir, "report", RegistrationFile)); err != nil {
		t.Errorf("缺少 %s: %v", RegistrationFile, err)
	}
}
