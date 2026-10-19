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
