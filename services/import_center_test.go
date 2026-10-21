package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// importCenterCore builds a synthetic archive. The real-archive helper needs
// PVF_TESTFILE; these tests must run everywhere, and the semantics under test
// (path / state / digest) do not depend on a retail archive.
func importCenterCore(t *testing.T, files map[string]string) *core {
	t.Helper()
	created := pvf.New()
	for path, text := range files {
		if _, err := created.AddFileText(path, text, pvf.TypeScript); err != nil {
			t.Fatalf("AddFileText(%s): %v", path, err)
		}
	}
	target := filepath.Join(t.TempDir(), "Script.pvf")
	if err := created.SaveAs(target); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	archive, err := pvf.Open(target)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := NewCore()
	if err := c.setArchive(archive); err != nil {
		t.Fatalf("setArchive: %v", err)
	}
	t.Cleanup(c.closeArchive)
	return c
}

// writeImportProduct lays out an extraction product the way Export does:
// manifest.json at the root, payloads under pvf/.
func writeImportProduct(t *testing.T, files []extraction.ExportedFile, payloads map[string]string) string {
	t.Helper()
	root := t.TempDir()
	data, err := json.MarshalIndent(extraction.Manifest{
		SchemaVersion: extraction.SemanticVersion,
		Source:        "test",
		Files:         files,
	}, "", "  ")
	if err != nil {
		t.Fatalf("编码 manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, extraction.ManifestName), data, 0o644); err != nil {
		t.Fatalf("写入 manifest: %v", err)
	}
	for dest, text := range payloads {
		target := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("创建目录: %v", err)
		}
		if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
			t.Fatalf("写入载荷 %s: %v", dest, err)
		}
	}
	return root
}

func importCenterArchiveText(t *testing.T, c *core, path string) string {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	index, ok := c.archive.Find(path)
	if !ok {
		t.Fatalf("归档缺少 %s", path)
	}
	text, err := c.archive.Text(index)
	if err != nil {
		t.Fatalf("Text(%s): %v", path, err)
	}
	return text
}

// TestImportCenterPreviewDoesNotTouchArchive pins B2: opening and editing a
// plan must leave the live archive byte-identical.
func TestImportCenterPreviewDoesNotTouchArchive(t *testing.T) {
	c := importCenterCore(t, map[string]string{
		"a/same.txt": "[same]\n\t1\n",
		"a/diff.txt": "[diff]\n\t1\n",
	})
	same := importCenterArchiveText(t, c, "a/same.txt")

	root := writeImportProduct(t, []extraction.ExportedFile{
		{Path: "a/same.txt", Dest: "a/same.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "a/diff.txt", Dest: "a/diff.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "a/new.txt", Dest: "a/new.txt", Form: "text", DataType: pvf.TypeScript},
	}, map[string]string{
		"pvf/a/same.txt": same,
		"pvf/a/diff.txt": "[diff]\n\t2\n",
		"pvf/a/new.txt":  "[new]\n\t3\n",
	})

	c.mu.RLock()
	revision := c.batchRevision
	c.mu.RUnlock()

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if preview.Counts.Total != 3 || preview.Counts.Unchanged != 1 ||
		preview.Counts.Overwrite != 1 || preview.Counts.New != 1 {
		t.Fatalf("计数不对: %+v", preview.Counts)
	}

	if got := preview.Counts.Selected; got != 1 {
		t.Fatalf("默认只勾选 new，got %d", got)
	}

	c.mu.RLock()
	modified := c.archive.ModifiedCount()
	current := c.batchRevision
	c.mu.RUnlock()
	if modified != 0 {
		t.Fatalf("预览改动了归档: ModifiedCount=%d", modified)
	}
	if current != revision {
		t.Fatalf("预览递增了 revision: %d -> %d", revision, current)
	}
	svc.Release(preview.ID)
	if _, err := svc.Page(preview.ID, 0, 10); err == nil {
		t.Fatal("Release 之后计划必须失效")
	}
}

// TestImportCenterExecuteAppliesText covers the two write paths: replace an
// existing entry and add a new one.
func TestImportCenterExecuteAppliesText(t *testing.T) {
	c := importCenterCore(t, map[string]string{"a/diff.txt": "[diff]\n\t1\n"})
	before := importCenterArchiveText(t, c, "a/diff.txt")

	root := writeImportProduct(t, []extraction.ExportedFile{
		{Path: "a/diff.txt", Dest: "a/diff.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "a/new.txt", Dest: "a/new.txt", Form: "text", DataType: pvf.TypeScript},
	}, map[string]string{
		"pvf/a/diff.txt": before + "[added]\n\t2\n",
		"pvf/a/new.txt":  "[new]\n\t3\n",
	})

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 覆盖默认不勾选；这里显式打开，验证 Selected 的语义。
	if _, err := svc.SetSelected(preview.ID, []string{"a/diff.txt"}, true); err != nil {
		t.Fatalf("SetSelected: %v", err)
	}
	result, err := svc.Execute(preview.ID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Written != 1 || result.Overwritten != 1 {
		t.Fatalf("写入计数不对: %+v", result)
	}
	// 内容比对按语义做：脚本的 token 编码不保证与手写文本逐字节一致，
	// 判据是「值变了且新内容在」，不是「等于我拼出来的那串」。
	if got := importCenterArchiveText(t, c, "a/diff.txt"); got == before || !strings.Contains(got, "[added]") {
		t.Fatalf("覆盖内容不对: %q", got)
	}
	if got := importCenterArchiveText(t, c, "a/new.txt"); !strings.Contains(got, "[new]") {
		t.Fatalf("新增内容不对: %q", got)
	}
	if _, err := svc.Execute(preview.ID); err == nil {
		t.Fatal("计划是一次性的，第二次执行必须失效")
	}
}

// TestImportCenterRejectsChangedPayload: the digest taken at plan time is what
// makes the preview a promise rather than a suggestion.
func TestImportCenterRejectsChangedPayload(t *testing.T) {
	c := importCenterCore(t, map[string]string{"keep.txt": "[keep]\n\t1\n"})
	root := writeImportProduct(t, []extraction.ExportedFile{
		{Path: "a/new.txt", Dest: "a/new.txt", Form: "text", DataType: pvf.TypeScript},
	}, map[string]string{"pvf/a/new.txt": "[new]\n\t1\n"})

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pvf", "a", "new.txt"), []byte("[new]\n\t2\n"), 0o644); err != nil {
		t.Fatalf("改写载荷: %v", err)
	}
	if _, err := svc.Execute(preview.ID); err == nil {
		t.Fatal("源文件被改动后必须拒绝执行")
	}
}

// TestImportCenterStaleAfterArchiveChange: a plan frozen against revision N
// must not apply over revision N+1.
func TestImportCenterStaleAfterArchiveChange(t *testing.T) {
	c := importCenterCore(t, map[string]string{"keep.txt": "[keep]\n\t1\n"})
	root := writeImportProduct(t, []extraction.ExportedFile{
		{Path: "a/new.txt", Dest: "a/new.txt", Form: "text", DataType: pvf.TypeScript},
	}, map[string]string{"pvf/a/new.txt": "[new]\n\t1\n"})

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 任何内容编辑都会递增 batchRevision（archiveGeneration 不在内容路径上）。
	c.mu.Lock()
	c.batchRevision++
	c.mu.Unlock()

	if _, err := svc.Execute(preview.ID); err == nil {
		t.Fatal("归档在预览之后变化，执行必须拒绝")
	}
}

func TestImportCenterPageCursor(t *testing.T) {
	c := importCenterCore(t, map[string]string{"keep.txt": "[keep]\n\t1\n"})
	files := make([]extraction.ExportedFile, 0, 3)
	payloads := map[string]string{}
	for _, name := range []string{"a/one.txt", "a/two.txt", "a/three.txt"} {
		files = append(files, extraction.ExportedFile{
			Path: name, Dest: "" + name, Form: "text", DataType: pvf.TypeScript})
		payloads["pvf/"+name] = "[" + name + "]\n\t1\n"
	}
	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: writeImportProduct(t, files, payloads)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first, err := svc.Page(preview.ID, 0, 2)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(first.Entries) != 2 || first.Next != 2 || first.Total != 3 {
		t.Fatalf("第一页不对: %+v", first)
	}
	second, err := svc.Page(preview.ID, first.Next, 2)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(second.Entries) != 1 || second.Next != -1 {
		t.Fatalf("第二页不对: %+v", second)
	}
}

// TestImportCenterAppliesRegistrationRows：导入的实体必须在目标表里有行，
// 否则文件进了归档、游戏里仍然找不到它。
func TestImportCenterAppliesRegistrationRows(t *testing.T) {
	c := importCenterCore(t, map[string]string{
		"equipment/equipment.lst":     "1008\t`character/old.equ`\n",
		"equipment/character/old.equ": "[old]\n\t1\n",
	})

	root := t.TempDir()
	payloads := map[string]string{
		"pvf/equipment/character/new.equ":        "[new]\n\t1\n",
		"pvf/_registries/equipment.lst.fragment": "2000\t`character/new.equ`\n",
	}
	for dest, text := range payloads {
		target := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := extraction.Manifest{
		SchemaVersion: extraction.SemanticVersion,
		Files: []extraction.ExportedFile{
			{Path: "equipment/character/new.equ", Dest: "equipment/character/new.equ",
				Form: "text", DataType: pvf.TypeScript},
		},
		Selection: &extraction.SelectionRecord{
			Mode:   "closed",
			Layout: extraction.LayoutMirror,
			Form:   extraction.FormText,
			RegistryFragments: []extraction.FragmentRecord{
				{Dest: "_registries/equipment.lst.fragment", ListPath: "equipment/equipment.lst", Rows: 1},
			},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extraction.ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if preview.Lists.Total != 1 || preview.Lists.New != 1 || preview.Lists.Selected != 1 {
		t.Fatalf("登记表计数不对: %+v", preview.Lists)
	}

	if _, err := svc.Execute(preview.ID); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	c.mu.RLock()
	listIndex, ok := c.archive.Find("equipment/equipment.lst")
	if !ok {
		c.mu.RUnlock()
		t.Fatal("归档里没有装备表")
	}
	pairs, err := c.archive.ListPairs(listIndex)
	c.mu.RUnlock()
	if err != nil {
		t.Fatalf("ListPairs: %v", err)
	}
	found := false
	for _, pair := range pairs {
		if pair.ID == "2000" && pair.Path == "character/new.equ" {
			found = true
		}
	}
	if !found {
		t.Fatalf("登记行没有写入: %+v", pairs)
	}
}

// TestImportCenterWritesRegistryOnly：文件全都没变（默认不勾选）但登记行需要写，
// 这时不能因为"没有可写入的文件"就提前返回。文件和登记是两条独立的写路径。
func TestImportCenterWritesRegistryOnly(t *testing.T) {
	c := importCenterCore(t, map[string]string{
		"equipment/equipment.lst":          "1008\t`character/old.equ`\n",
		"equipment/character/existing.equ": "[existing]\n\t1\n",
	})
	existing := importCenterArchiveText(t, c, "equipment/character/existing.equ")

	root := t.TempDir()
	payloads := map[string]string{
		"pvf/equipment/character/existing.equ":   existing,
		"pvf/_registries/equipment.lst.fragment": "2000\t`character/existing.equ`\n",
	}
	for dest, text := range payloads {
		target := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := extraction.Manifest{
		SchemaVersion: extraction.SemanticVersion,
		Files: []extraction.ExportedFile{
			{Path: "equipment/character/existing.equ", Dest: "equipment/character/existing.equ",
				Form: "text", DataType: pvf.TypeScript},
		},
		Selection: &extraction.SelectionRecord{
			Mode: "closed", Layout: extraction.LayoutMirror, Form: extraction.FormText,
			RegistryFragments: []extraction.FragmentRecord{
				{Dest: "_registries/equipment.lst.fragment", ListPath: "equipment/equipment.lst", Rows: 1},
			},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extraction.ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewImportCenterService(c)
	preview, err := svc.Open(ImportCenterRequest{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if preview.Counts.Selected != 0 {
		t.Fatalf("同一份文本不该被勾选: %+v", preview.Counts)
	}
	if preview.Lists.Selected != 1 {
		t.Fatalf("登记行应默认勾选: %+v", preview.Lists)
	}
	result, err := svc.Execute(preview.ID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Written != 0 || result.ListRows != 1 {
		t.Fatalf("只写登记行时结果不对: %+v", result)
	}

	c.mu.RLock()
	listIndex, ok := c.archive.Find("equipment/equipment.lst")
	if !ok {
		c.mu.RUnlock()
		t.Fatal("归档里没有装备表")
	}
	pairs, err := c.archive.ListPairs(listIndex)
	c.mu.RUnlock()
	if err != nil {
		t.Fatalf("ListPairs: %v", err)
	}
	found := false
	for _, pair := range pairs {
		if pair.ID == "2000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("只勾登记行时没有写入: %+v", pairs)
	}
}
