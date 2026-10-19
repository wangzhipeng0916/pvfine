package services

import (
	"errors"
	"strings"
	"testing"
)

func TestArchiveDiffExportDirectSnapshotAndDirection(t *testing.T) {
	c, diff, path := diffFixture(t)
	changed, _ := c.archive.Find("equipment/changed.equ")
	deleted, _ := c.archive.Find("equipment/deleted.equ")
	if _, err := c.archive.RemoveFiles([]int32{deleted}); err != nil {
		t.Fatal(err)
	}
	changed, _ = c.archive.Find("equipment/changed.equ")
	addDiffText(t, c.archive, "equipment/new.equ", "[name]\n`new`")
	c.archive.AddFile("data/new.bin", []byte{0, 255, 1}, 99)
	p, err := diff.Start(ArchiveDiffRequest{Path: path, Drafts: []ArchiveDiffDraft{{FileIndex: changed, Path: "equipment/changed.equ", Text: "[name]\n`draft`"}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, diff, p.ID); status.State != "ready" {
		t.Fatalf("status=%+v", status)
	}
	exports := NewExportService(c)
	r := ExportRequest{Source: "archive-diff", DiffID: p.ID, Mode: "direct"}
	preview, err := exports.Prepare(r)
	if err != nil || preview.FileCount != 4 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	// Editing the workspace after comparison must not change the snapshot.
	if err := c.archive.SetText(changed, "[name]\n`live edit`"); err != nil {
		t.Fatal(err)
	}
	c.batchRevision++
	root, err := exports.executeTo(preview.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := readExportFile(t, root, "equipment/changed.equ"); !strings.Contains(got, "draft") || strings.Contains(got, "live edit") {
		t.Fatalf("export did not use the draft snapshot: %q", got)
	}
	if got := readExportFile(t, root, "data/new.bin"); got != string([]byte{0, 255, 1}) {
		t.Fatal("binary payload changed")
	}
	// Direct export retains the existing behavior of exporting deleted files
	// from the baseline. It writes files, never deletion instructions.
	readExportFile(t, root, "equipment/deleted.equ")
	r.Reverse = true
	preview, err = exports.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	root, err = exports.executeTo(preview.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := readExportFile(t, root, "equipment/changed.equ"); !strings.Contains(got, "old") || strings.Contains(got, "draft") {
		t.Fatalf("reverse export used wrong side: %q", got)
	}
	preview, err = exports.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	diff.Release()
	if _, err := exports.executeTo(preview.ID, t.TempDir()); !errors.Is(err, ErrExportStale) {
		t.Fatalf("released comparison was exportable: %v", err)
	}
}

func TestArchiveDiffExportModTablesAndDependencies(t *testing.T) {
	c, exports := exportFixture(t)
	diff := NewArchiveDiffService(c)
	path := c.archive.SourcePath()
	stringsIndex, _ := c.archive.Find("string/demo.str")
	if err := c.archive.SetText(stringsIndex, "name>snapshot name\nempty>\nkeep>保留"); err != nil {
		t.Fatal(err)
	}
	scriptIndex, _ := c.archive.Find("stackable/demo.stk")
	if err := c.archive.SetText(scriptIndex, "[name]\n{8=`<1::name>`}\n[price]\n222"); err != nil {
		t.Fatal(err)
	}
	p, err := diff.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, diff, p.ID); status.State != "ready" {
		t.Fatalf("status=%+v", status)
	}
	r := modRequest("archive-diff")
	r.DiffID, r.IncludeDependencies = p.ID, true
	preview, err := exports.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	root, err := exports.executeTo(preview.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := readExportFile(t, root, "merge/string/demo.str"); got != "name>snapshot name\n" {
		t.Fatalf("wrong table delta: %q", got)
	}
	if got := readExportFile(t, root, "pvf/stackable/demo.stk"); !strings.Contains(got, "222") {
		t.Fatalf("wrong script side: %q", got)
	}
	readExportFile(t, root, "merge/list/n_string.lst")
	r.Reverse = true
	preview, err = exports.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	root, err = exports.executeTo(preview.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := readExportFile(t, root, "merge/string/demo.str"); !strings.Contains(got, "测试物品") || strings.Contains(got, "snapshot name") {
		t.Fatalf("wrong reverse table delta: %q", got)
	}
	if got := readExportFile(t, root, "pvf/stackable/demo.stk"); !strings.Contains(got, "100") {
		t.Fatalf("wrong reverse script: %q", got)
	}
}

func TestArchiveDiffExportDeletesReverseCacheAndRefresh(t *testing.T) {
	c, diff, path := diffFixture(t)
	deleted, _ := c.archive.Find("equipment/deleted.equ")
	if _, err := c.archive.RemoveFiles([]int32{deleted}); err != nil {
		t.Fatal(err)
	}
	changed, _ := c.archive.Find("equipment/changed.equ")
	if err := c.archive.SetText(changed, "[name]\n`new target`"); err != nil {
		t.Fatal(err)
	}
	addDiffText(t, c.archive, "equipment/empty.equ", "")
	p, err := diff.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, diff, p.ID); status.State != "ready" {
		t.Fatalf("status=%+v", status)
	}
	exports := NewExportService(c)
	r := modRequest("archive-diff")
	r.DiffID = p.ID
	checkpoint := c.archive.MutationCheckpoint()
	preview, err := exports.Prepare(r)
	if err != nil || preview.SkippedDeletes != 1 || preview.FileCount != 2 {
		t.Fatalf("forward preview=%+v err=%v", preview, err)
	}
	original := exports.plan
	r.Reverse = true
	reversed, err := exports.Prepare(r)
	if err != nil || reversed.SkippedDeletes != 1 || reversed.FileCount != 2 {
		t.Fatalf("reverse preview=%+v err=%v", reversed, err)
	}
	if exports.plan == original {
		t.Fatal("reverse reused the forward export plan")
	}
	root, err := exports.executeTo(reversed.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if text := readExportFile(t, root, "pvf/equipment/changed.equ"); !strings.Contains(text, "old") || strings.Contains(text, "new target") {
		t.Fatalf("reverse export reused forward content: %q", text)
	}
	readExportFile(t, root, "pvf/equipment/deleted.equ")
	if c.archive.MutationCheckpoint() != checkpoint {
		t.Fatal("export mutated workspace")
	}
	preview, err = exports.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	next, err := diff.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.executeTo(preview.ID, t.TempDir()); !errors.Is(err, ErrExportStale) {
		t.Fatalf("refresh did not invalidate previous export: %v", err)
	}
	waitDiff(t, diff, next.ID)
}

func TestArchiveDiffExportSelectionAndScopeFilter(t *testing.T) {
	c, diff, path := diffFixture(t)
	changed, _ := c.archive.Find("equipment/changed.equ")
	deleted, _ := c.archive.Find("equipment/deleted.equ")
	if _, err := c.archive.RemoveFiles([]int32{deleted}); err != nil {
		t.Fatal(err)
	}
	changed, _ = c.archive.Find("equipment/changed.equ")
	addDiffText(t, c.archive, "equipment/new.equ", "[name]\n`new`")
	c.archive.AddFile("data/new.bin", []byte{0, 255, 1}, 99)
	p, err := diff.Start(ArchiveDiffRequest{
		Path: path,
		Drafts: []ArchiveDiffDraft{
			{FileIndex: changed, Path: "equipment/changed.equ", Text: "[name]\n`draft`"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, diff, p.ID); status.State != "ready" {
		t.Fatalf("status=%+v", status)
	}

	exports := NewExportService(c)

	// 1. 过滤单个文件 scope
	rSingle := ExportRequest{
		Source: "archive-diff",
		DiffID: p.ID,
		Mode:   "direct",
		Scopes: []string{"equipment/changed.equ"},
	}
	previewSingle, err := exports.Prepare(rSingle)
	if err != nil || previewSingle.FileCount != 1 {
		t.Fatalf("previewSingle=%+v err=%v", previewSingle, err)
	}
	if previewSingle.Files[0].Path != "equipment/changed.equ" {
		t.Fatalf("unexpected export file: %s", previewSingle.Files[0].Path)
	}

	// 2. 过滤目录 scope (equipment/)
	rDir := ExportRequest{
		Source: "archive-diff",
		DiffID: p.ID,
		Mode:   "direct",
		Scopes: []string{"equipment"},
	}
	previewDir, err := exports.Prepare(rDir)
	if err != nil || previewDir.FileCount != 3 {
		t.Fatalf("previewDir=%+v err=%v", previewDir, err)
	}

	// 3. 不存在 scope
	rEmpty := ExportRequest{
		Source: "archive-diff",
		DiffID: p.ID,
		Mode:   "direct",
		Scopes: []string{"nonexistent/path"},
	}
	if _, err := exports.Prepare(rEmpty); err == nil {
		t.Fatal("expected error for empty matched scopes")
	}
}
