package importing

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

const equipListPath = "equipment/equipment.lst"

func listRowText(id, entryPath string) string {
	return id + "\t`" + entryPath + "`\n"
}

func planRows(t *testing.T, archive *pvf.Archive, rows []FragmentRow, candidates []string) []ListEntry {
	t.Helper()
	entries, err := PlanListRows(archive, rows, candidates)
	if err != nil {
		t.Fatalf("PlanListRows: %v", err)
	}
	return entries
}

func onlyEntry(t *testing.T, entries []ListEntry) ListEntry {
	t.Helper()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %#v", len(entries), entries)
	}
	return entries[0]
}

func TestPlanListRowsNew(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/old.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	fileIndex, ok := archive.Find("equipment/character/a.equ")
	if !ok {
		t.Fatal("target file missing from fixture")
	}
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
	}, nil))

	if entry.State != ListNew || !entry.Selected || entry.Resolution != ResolveSkip {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.ListIndex < 0 || entry.ListPath != equipListPath {
		t.Fatalf("list location = %q (%d)", entry.ListPath, entry.ListIndex)
	}
	if entry.TargetIndex != fileIndex {
		t.Fatalf("TargetIndex = %d, want %d", entry.TargetIndex, fileIndex)
	}
	// 90 布局的表里写相对表目录的形式。
	if entry.TargetPath != "character/a.equ" {
		t.Fatalf("TargetPath = %q", entry.TargetPath)
	}
}

func TestPlanListRowsRegistered(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("2000", "character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
	}, nil))

	if entry.State != ListRegistered {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.Selected {
		t.Fatal("registered row must not be selected")
	}
}

func TestPlanListRowsIDTaken(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("2000", "character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
		archiveEntry{path: "equipment/character/b.equ", text: "b"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/b.equ"},
	}, nil))

	if entry.State != ListIDTaken {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.ConflictOf != "character/a.equ" {
		t.Fatalf("ConflictOf = %q", entry.ConflictOf)
	}
	if entry.Selected || entry.Resolution != ResolveSkip {
		t.Fatalf("id-taken default = selected %v, resolution %q", entry.Selected, entry.Resolution)
	}
	if entry.TargetPath != "character/b.equ" {
		t.Fatalf("TargetPath = %q", entry.TargetPath)
	}
}

func TestPlanListRowsDuplicate(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("2000", "character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "3000", Path: "character/a.equ"},
	}, nil))

	if entry.State != ListDuplicate {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.ConflictOf != "2000" {
		t.Fatalf("ConflictOf = %q", entry.ConflictOf)
	}
	if entry.Selected || entry.Resolution != ResolveSkip {
		t.Fatalf("duplicate default = selected %v, resolution %q", entry.Selected, entry.Resolution)
	}
}

func TestPlanListRowsDangling(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("2000", "character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "3000", Path: "character/missing.equ"},
	}, nil))

	if entry.State != ListDangling {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.TargetIndex != -1 || entry.TargetPath != "" {
		t.Fatalf("dangling target = %d %q", entry.TargetIndex, entry.TargetPath)
	}
	if entry.Selected {
		t.Fatal("dangling row must not be selected")
	}
}

func TestPlanListRowsNoTable(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: "monster/monster.lst", ID: "2000", Path: "equipment/character/a.equ"},
	}, nil))

	if entry.State != ListNoTable {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.ListIndex != -1 || entry.ListPath != "monster/monster.lst" {
		t.Fatalf("no-table location = %q (%d)", entry.ListPath, entry.ListIndex)
	}
	if entry.Selected {
		t.Fatal("no-table row must not be selected")
	}
}

// TestPlanListRowsStaleRowIsIDTaken 覆盖 D5：表里有一行同 ID，但它指向的文件
// 已经不存在。索引只存二进制索引的话，这一行会退化成「ID 不存在」，新行被当成
// new 再追加一条同 ID 行。
func TestPlanListRowsStaleRowIsIDTaken(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("2000", "character/old.equ")},
		archiveEntry{path: "equipment/character/new.equ", text: "new"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/new.equ"},
	}, nil))

	if entry.State != ListIDTaken {
		t.Fatalf("state = %q, want id-taken", entry.State)
	}
	if entry.ConflictOf != "character/old.equ" {
		t.Fatalf("ConflictOf = %q", entry.ConflictOf)
	}
}

// TestPlanListRowsPagedLayout 覆盖 90→110：表实际在 `list/equipment.lst`，
// 片段按 90 的表名与根相对路径书写，仍要定位到表并解析出目标。
func TestPlanListRowsPagedLayout(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: "list/equipment.lst", text: listRowText("1008", "equipment/character/old.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	fileIndex, ok := archive.Find("equipment/character/a.equ")
	if !ok {
		t.Fatal("target file missing from fixture")
	}
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: "equipment/equipment.lst", ID: "2000", Path: "equipment/character/a.equ"},
	}, nil))

	if entry.State != ListNew {
		t.Fatalf("state = %q", entry.State)
	}
	if entry.ListPath != "list/equipment.lst" {
		t.Fatalf("ListPath = %q", entry.ListPath)
	}
	if entry.TargetIndex != fileIndex {
		t.Fatalf("TargetIndex = %d, want %d", entry.TargetIndex, fileIndex)
	}
	// 110 的表保留归档根相对写法，不能套用 90 的相对表目录形式。
	if entry.TargetPath != "equipment/character/a.equ" {
		t.Fatalf("TargetPath = %q", entry.TargetPath)
	}
}

// TestPlanListRowsCandidatePaths 覆盖预览期：文件还没进归档，目标只能靠候选集
// 解析；不传候选集时同一行必须是 dangling，而不是被误判成可写。
func TestPlanListRowsCandidatePaths(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: "list/equipment.lst", text: listRowText("1008", "equipment/character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	rows := []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "Equipment/Character/New.equ"},
	}

	without := onlyEntry(t, planRows(t, archive, rows, nil))
	if without.State != ListDangling {
		t.Fatalf("state without candidates = %q, want dangling", without.State)
	}

	with := onlyEntry(t, planRows(t, archive, rows, []string{"equipment/character/new.equ"}))
	if with.State != ListNew || !with.Selected {
		t.Fatalf("state with candidates = %q selected=%v", with.State, with.Selected)
	}
	if with.TargetIndex != -1 {
		t.Fatalf("candidate TargetIndex = %d, want -1", with.TargetIndex)
	}
	if with.TargetPath != "equipment/character/new.equ" {
		t.Fatalf("candidate TargetPath = %q", with.TargetPath)
	}
}

func TestPlanListRowsDropsDuplicateFragmentRows(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/a.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
	)
	entries := planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
	}, nil)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestApplyListRowsWritesNew(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/old.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
		archiveEntry{path: "equipment/character/b.equ", text: "b"},
		archiveEntry{path: "equipment/character/c.equ", text: "c"},
	)
	rows := []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
		{ListPath: equipListPath, ID: "2001", Path: "character/b.equ"},
		{ListPath: equipListPath, ID: "2002", Path: "character/c.equ"},
	}
	entries := planRows(t, archive, rows, nil)
	for _, entry := range entries {
		if entry.State != ListNew || !entry.Selected {
			t.Fatalf("entry %s = %#v", entry.ID, entry)
		}
	}

	changed, err := ApplyListRows(archive, entries)
	if err != nil {
		t.Fatalf("ApplyListRows: %v", err)
	}
	if len(changed) != 1 || changed[0] != equipListPath {
		t.Fatalf("changed = %v", changed)
	}

	pairs, err := archive.ListPairs(entries[0].ListIndex)
	if err != nil {
		t.Fatalf("ListPairs: %v", err)
	}
	if len(pairs) != 4 {
		t.Fatalf("pairs = %#v", pairs)
	}
	written := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		written[pair.ID] = pair.Path
	}
	if written["1008"] != "character/old.equ" {
		t.Fatalf("existing row changed: %q", written["1008"])
	}
	for _, entry := range entries {
		if written[entry.ID] != entry.TargetPath {
			t.Fatalf("%s written as %q, TargetPath = %q", entry.ID, written[entry.ID], entry.TargetPath)
		}
	}
}

// TestApplyListRowsOverwriteKeepsRowPosition 复用 metadata_test.go 的判据：
// 未涉及的行必须逐字节不变，被改行的 id token 也必须保留。
func TestApplyListRowsOverwriteKeepsRowPosition(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/a.equ") + listRowText("1009", "character/b.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
		archiveEntry{path: "equipment/character/b.equ", text: "b"},
		archiveEntry{path: "equipment/character/c.equ", text: "c"},
	)
	listIndex, ok := archive.FindList(equipListPath)
	if !ok {
		t.Fatal("list missing")
	}
	before, err := archive.RawBytes(listIndex)
	if err != nil {
		t.Fatalf("RawBytes: %v", err)
	}

	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "1008", Path: "character/c.equ"},
	}, nil))
	if entry.State != ListIDTaken {
		t.Fatalf("state = %q", entry.State)
	}
	entry.Selected = true
	entry.Resolution = ResolveOverwrite

	if _, err := ApplyListRows(archive, []ListEntry{entry}); err != nil {
		t.Fatalf("ApplyListRows: %v", err)
	}
	after, err := archive.RawBytes(listIndex)
	if err != nil {
		t.Fatalf("RawBytes: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("payload length = %d, want %d (overwrite must stay in place)", len(after), len(before))
	}
	if !bytes.Equal(after[:5], before[:5]) {
		t.Fatalf("id token changed: before=%v after=%v", before[:5], after[:5])
	}
	if !bytes.Equal(after[10:], before[10:]) {
		t.Fatalf("untouched row changed: before=%v after=%v", before[10:], after[10:])
	}

	pairs, err := archive.ListPairs(listIndex)
	if err != nil {
		t.Fatalf("ListPairs: %v", err)
	}
	if len(pairs) != 2 || pairs[0].ID != "1008" || pairs[0].Path != "character/c.equ" {
		t.Fatalf("pairs = %#v", pairs)
	}
}

func TestApplyListRowsRemoveOld(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/a.equ") + listRowText("1009", "character/b.equ")},
		archiveEntry{path: "equipment/character/a.equ", text: "a"},
		archiveEntry{path: "equipment/character/b.equ", text: "b"},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/a.equ"},
	}, nil))
	if entry.State != ListDuplicate || entry.ConflictOf != "1008" {
		t.Fatalf("entry = %#v", entry)
	}
	entry.Selected = true
	entry.Resolution = ResolveRemoveOld

	changed, err := ApplyListRows(archive, []ListEntry{entry})
	if err != nil {
		t.Fatalf("ApplyListRows: %v", err)
	}
	if len(changed) != 1 || changed[0] != equipListPath {
		t.Fatalf("changed = %v", changed)
	}

	pairs, err := archive.ListPairs(entry.ListIndex)
	if err != nil {
		t.Fatalf("ListPairs: %v", err)
	}
	written := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		written[pair.ID] = pair.Path
	}
	if _, exists := written["1008"]; exists {
		t.Fatalf("old id survived: %#v", pairs)
	}
	if written["1009"] != "character/b.equ" {
		t.Fatalf("unrelated row changed: %#v", pairs)
	}
	if written["2000"] != entry.TargetPath {
		t.Fatalf("new row = %q, TargetPath = %q", written["2000"], entry.TargetPath)
	}
}

func TestReadFragments(t *testing.T) {
	root := t.TempDir()
	dest := extraction.RegistryFragmentDir + "/equipment.lst.fragment"
	// 片段落在 <root>/pvf/_registries/：Dest 相对 manifest.subdir，不是相对导出根。
	target := filepath.Join(root, extraction.DefaultSubdir, filepath.FromSlash(dest))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := listRowText("1008", "character/a.equ") + "# 注释与空行必须被容忍\n\n" + listRowText("1009", "character/b.equ")
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manifest := &extraction.Manifest{Selection: &extraction.SelectionRecord{
		RegistryFragments: []extraction.FragmentRecord{{
			Dest: dest, ListPath: equipListPath, Rows: 2,
		}},
	}}

	rows, err := ReadFragments(root, manifest)
	if err != nil {
		t.Fatalf("ReadFragments: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].ListPath != equipListPath || rows[0].ID != "1008" || rows[0].Path != "character/a.equ" {
		t.Fatalf("row 0 = %#v", rows[0])
	}
	if rows[1].ID != "1009" || rows[1].Path != "character/b.equ" {
		t.Fatalf("row 1 = %#v", rows[1])
	}
}

func TestReadFragmentsMissingFile(t *testing.T) {
	root := t.TempDir()
	manifest := &extraction.Manifest{Selection: &extraction.SelectionRecord{
		RegistryFragments: []extraction.FragmentRecord{{
			Dest:     filepath.ToSlash(filepath.Join(extraction.RegistryFragmentDir, "equipment.lst.fragment")),
			ListPath: equipListPath,
			Rows:     1,
		}},
	}}
	if _, err := ReadFragments(root, manifest); err == nil {
		t.Fatal("missing fragment file must be an error")
	}
}

func TestReadFragmentsWithoutSelection(t *testing.T) {
	root := t.TempDir()
	rows, err := ReadFragments(root, &extraction.Manifest{})
	if err != nil || rows != nil {
		t.Fatalf("ReadFragments = %#v, %v", rows, err)
	}
	if rows, err := ReadFragments(root, nil); err != nil || rows != nil {
		t.Fatalf("ReadFragments(nil) = %#v, %v", rows, err)
	}
}

// TestPlanListRowsMatchesCandidateAcrossLayout：90 布局的表把路径写成相对表目录
// （`character/new.equ`），而候选集里存的是归档路径（`equipment/character/new.equ`）。
// 不把两种写法统一，实体行会全部判成 dangling——这个功能就等于没做。
func TestPlanListRowsMatchesCandidateAcrossLayout(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: equipListPath, text: listRowText("1008", "character/old.equ")},
	)
	entry := onlyEntry(t, planRows(t, archive, []FragmentRow{
		{ListPath: equipListPath, ID: "2000", Path: "character/new.equ"},
	}, []string{"equipment/character/new.equ"}))

	if entry.State != ListNew || !entry.Selected {
		t.Fatalf("候选文件应判 new: %#v", entry)
	}
	if entry.TargetIndex != -1 {
		t.Fatalf("候选还没进归档，TargetIndex 应为 -1，got %d", entry.TargetIndex)
	}
	if entry.TargetPath != "character/new.equ" {
		t.Fatalf("TargetPath 应是表内写法：%q", entry.TargetPath)
	}
}
