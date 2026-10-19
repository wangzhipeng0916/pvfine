package extraction

import (
	"os"
	"path/filepath"
	"pvfine/internal/annotations"
	"strings"
	"testing"
)

// TestRegistryFragmentsCarryOnlyInvolvedRows is the contract of the fragment
// artifact: the .lst rows the extraction actually used, in .lst line format,
// plus a marked note for ids the table does not hold. Whole tables are
// deliberately not copied — a registry must stay complete to remain loadable.
func TestRegistryFragmentsCarryOnlyInvolvedRows(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "map.passiveObject", Extensions: []string{".map"}, Section: "passive object",
		TokenTypes: []int32{0}, Record: &Record{Width: 4, RefOffsets: []int{0}},
		Target: Target{Kind: TargetRegistry, Relation: "被动对象"},
	}}}
	f := newFixtureFrom(t, []entry{
		// 10 已登记；99 未登记，用于验证"缺失 ID"也留在同一份产物里。
		{"map/forest/uses.map", "[passive object]\n\t10\n\t1\n\t2\n\t3\n\t99\n\t4\n\t5\n\t6\n"},
	}, doc)
	f.specs["被动对象"] = annotations.RelationSpec{
		ListPath: "passiveobject/passiveobject.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	scanner := f.scannerWith(t, doc)
	seed, _ := f.archive.Find("map/forest/uses.map")
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	exported := []int32{}
	for _, node := range closure.Nodes {
		exported = append(exported, node.Index)
	}
	fragments := BuildRegistryFragments(scanner, exported)
	if len(fragments) != 1 {
		t.Fatalf("应产出 1 个片段, got %d: %+v", len(fragments), fragments)
	}
	fragment := fragments[0]
	wantRow := "10\t`obj/door.obj`"
	if len(fragment.Rows) != 1 || fragment.Rows[0] != wantRow {
		t.Errorf("片段行应为 %q, got %v", wantRow, fragment.Rows)
	}
	if len(fragment.MissingIDs) != 1 || fragment.MissingIDs[0] != "99" {
		t.Errorf("未登记 ID 应记在片段里, got %v", fragment.MissingIDs)
	}
	if dest := fragment.Dest(); dest != "_registries/passiveobject.lst.fragment" {
		t.Errorf("片段落点异常: %s", dest)
	}

	// 整表不得被带出：片段行数必须远小于表行数，且不含未涉及的 ID（11）。
	plan, err := BuildPlan(f.archive, scanner.Catalog(), closure, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := Export(f.archive, closure, ExportOptions{
		Dir: root, Plan: plan, Scanner: scanner,
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, DefaultSubdir, filepath.FromSlash(fragment.Dest())))
	if err != nil {
		t.Fatalf("片段未落盘: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, wantRow) {
		t.Errorf("片段缺少已登记行:\n%s", text)
	}
	if strings.Contains(text, "11\t`obj/crate.obj`") {
		t.Errorf("片段带上了本次未涉及的登记行:\n%s", text)
	}
	if !strings.Contains(text, "# 99") {
		t.Errorf("片段未标注未登记 ID:\n%s", text)
	}

	// 关闭开关必须真的关掉它。
	quiet := t.TempDir()
	if _, err := Export(f.archive, closure, ExportOptions{
		Dir: quiet, Plan: plan, Scanner: scanner,
		NoFragments: true,
	}); err != nil {
		t.Fatalf("Export(no fragments): %v", err)
	}
	if _, err := os.Stat(filepath.Join(quiet, DefaultSubdir, RegistryFragmentDir)); err == nil {
		t.Error("NoFragments 下仍写出了 _registries/")
	}
}

// TestRegistryFragmentsRegisterEntitiesThemselves is the import half of the
// artifact: the package must say how its own files are registered. Files
// without their .lst row are invisible to the game, and after the fact the row
// can only be recovered from the source archive — which the recipient of a
// package does not have.
func TestRegistryFragmentsRegisterEntitiesThemselves(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "action.useAnimation", Extensions: []string{".obj"}, Section: "use animation",
		TokenTypes: []int32{6}, Target: Target{Kind: TargetEntry},
	}}}
	f := newFixtureFrom(t, nil, doc)
	scanner := f.scannerWith(t, doc)
	// 10 已登记（obj/door.obj）；door.ani 只有路径引用、注册表里没有它——
	// 这是正常情形，不能因此替它编一行。
	seed, _ := f.archive.Find("passiveobject/obj/door.obj")
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	exported := []int32{}
	for _, node := range closure.Nodes {
		exported = append(exported, node.Index)
	}
	fragments := BuildRegistryFragments(scanner, exported)
	if len(fragments) != 1 {
		t.Fatalf("应产出 1 个片段, got %d: %+v", len(fragments), fragments)
	}
	fragment := fragments[0]
	if want := "10\t`obj/door.obj`"; len(fragment.EntityRows) != 1 || fragment.EntityRows[0] != want {
		t.Errorf("实体登记行应为 %q, got %v", want, fragment.EntityRows)
	}
	if len(fragment.Rows) != 0 {
		t.Errorf("本次没有登记表引用，Rows 应为空, got %v", fragment.Rows)
	}
	body := fragmentBody(fragment)
	if !strings.Contains(body, "本次导出的实体本身") || !strings.Contains(body, "10\t`obj/door.obj`") {
		t.Errorf("片段正文缺少实体登记行:\n%s", body)
	}
	if strings.Contains(body, "door.ani") {
		t.Errorf("未登记的实体不得被编出行来:\n%s", body)
	}

	// 同一行既是"被引用"又是"实体自身"时只写一次（map 引用 10，door.obj 本身也在
	// 导出集里）。
	base := newFixture(t)
	baseScanner := base.scanner(t)
	mapSeed, _ := base.archive.Find("map/forest/a.map")
	baseClosure, err := baseScanner.Closure([]int32{mapSeed}, Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	baseExported := []int32{}
	for _, node := range baseClosure.Nodes {
		baseExported = append(baseExported, node.Index)
	}
	for _, fragment := range BuildRegistryFragments(baseScanner, baseExported) {
		for _, row := range fragment.EntityRows {
			if row == "10\t`obj/door.obj`" {
				t.Errorf("同一行在两段里各写一次: Rows=%v EntityRows=%v", fragment.Rows, fragment.EntityRows)
			}
		}
	}
}

// TestRegistrationIndexCoversEveryTable pins the reverse index the fragment
// uses: a path registered in two tables is reported in both, because choosing
// one would hide a row the reader may need.
func TestRegistrationIndexCoversEveryTable(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"monster/monster.lst", "7\t`../passiveobject/obj/door.obj`\n"},
	}, testDocument())
	f.specs["怪物"] = annotations.RelationSpec{
		ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	scanner := f.scannerWith(t, testDocument())
	index := scanner.registrationIndex()
	rows := index[normalizeRegistryPath("passiveobject/obj/door.obj")]
	if len(rows) != 2 {
		t.Fatalf("door.obj 应在两张表里各有一行, got %+v", rows)
	}
	tables := map[string]string{}
	for _, row := range rows {
		tables[row.listPath] = row.row
	}
	if tables["passiveobject/passiveobject.lst"] != "10\t`obj/door.obj`" {
		t.Errorf("被动对象表行不对: %+v", tables)
	}
	if tables["monster/monster.lst"] != "7\t`../passiveobject/obj/door.obj`" {
		t.Errorf("怪物表行不对: %+v", tables)
	}
}
