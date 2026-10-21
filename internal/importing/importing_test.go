package importing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
	"pvfine/internal/rendering"
)

// archiveEntry 是合成归档里的一个文本条目。
type archiveEntry struct {
	path     string
	text     string
	dataType int32
}

// newArchive 建一个真实落盘再打开的归档。Archive 的文本解码依赖于打开时的
// 客户端版本与渲染规则，只有走完 SaveAs → Open 才与生产路径一致。
func newArchive(t *testing.T, entries ...archiveEntry) *pvf.Archive {
	t.Helper()
	created := pvf.New()
	for _, entry := range entries {
		dataType := entry.dataType
		if dataType == 0 {
			dataType = pvf.TypeScript
		}
		if _, err := created.AddFileText(entry.path, entry.text, dataType); err != nil {
			t.Fatalf("AddFileText(%s): %v", entry.path, err)
		}
	}
	file := filepath.Join(t.TempDir(), "Script.pvf")
	if err := created.SaveAs(file); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return archive
}

// writeManifest 模拟提取侧写出的 manifest.json。
func writeManifest(t *testing.T, root string, files []extraction.ExportedFile) {
	t.Helper()
	data, err := json.MarshalIndent(extraction.Manifest{
		SchemaVersion: extraction.SemanticVersion,
		Files:         files,
	}, "", "  ")
	if err != nil {
		t.Fatalf("编码 manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, extraction.ManifestName), data, 0o644); err != nil {
		t.Fatalf("写入 manifest: %v", err)
	}
}

// writePayload 在提取根目录下按 dest 写载荷。
func writePayload(t *testing.T, root, dest, text string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(dest))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
		t.Fatalf("写入载荷 %s: %v", dest, err)
	}
}

func mustSource(t *testing.T, root string) *Source {
	t.Helper()
	source, err := FromExtraction(root)
	if err != nil {
		t.Fatalf("FromExtraction: %v", err)
	}
	return source
}

func mustEntries(t *testing.T, archive *pvf.Archive, source *Source) []FileEntry {
	t.Helper()
	entries, err := Classify(archive, source)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return entries
}

// archiveText 返回归档条目经 a.Text 解码后的文本，也就是提取侧会写到磁盘的内容。
func archiveText(t *testing.T, archive *pvf.Archive, archivePath string) string {
	t.Helper()
	index, ok := archive.Find(archivePath)
	if !ok {
		t.Fatalf("归档缺少 %s", archivePath)
	}
	text, err := archive.Text(index)
	if err != nil {
		t.Fatalf("Text(%s): %v", archivePath, err)
	}
	return text
}

// TestClassifyKeepsRawEntriesOutOfScope 覆盖判据 1：raw 条目出现在结果里，但标 unsupported。
// raw 载荷故意不落盘，用来证明「form 判定」发生在读盘之前。
func TestClassifyKeepsRawEntriesOutOfScope(t *testing.T) {
	root := t.TempDir()
	one := "[one]\n\t1\n"
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/one.txt", Dest: "a/one.txt", Size: len(one), Form: "text",
			DataType: pvf.TypeScript, Category: "装备", AutoCompleted: true},
		{Path: "a/two.txt", Dest: "a/two.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "ani/idle.ani", Dest: "ani/idle.ani", Form: "raw"},
	})
	writePayload(t, root, "pvf/a/one.txt", one)
	writePayload(t, root, "pvf/a/two.txt", "[two]\n\t2\n")

	source := mustSource(t, root)
	if source.Kind != "extraction" || source.Root != root || source.Manifest == nil {
		t.Fatalf("Source 元信息不对: %+v", source)
	}
	if len(source.Candidates) != 3 {
		t.Fatalf("Candidate 数 = %d, want 3", len(source.Candidates))
	}
	first := source.Candidates[0]
	if first.ArchivePath != "a/one.txt" || first.Origin != "manifest" ||
		first.Size != int64(len(one)) || first.Category != "装备" || !first.AutoCompleted {
		t.Fatalf("Candidate 字段没有从 manifest 完整带出: %+v", first)
	}
	if source.Candidates[2].Form != "raw" {
		t.Fatalf("raw 条目的 form 丢失: %+v", source.Candidates[2])
	}

	archive := newArchive(t, archiveEntry{path: "unrelated/keep.txt", text: "[keep]\n\t1\n"})
	entries := mustEntries(t, archive, source)
	if len(entries) != 3 {
		t.Fatalf("FileEntry 数 = %d, want 3", len(entries))
	}
	if entries[2].State != FileUnsupported || entries[2].Selected {
		t.Fatalf("raw 条目应为未勾选的 unsupported, got %+v", entries[2])
	}
	if !strings.Contains(entries[2].Reason, "二进制") {
		t.Fatalf("raw 条目的 Reason 没有说明原因: %q", entries[2].Reason)
	}
}

// TestClassifyThreeStates 覆盖判据 2：new / unchanged / overwrite 三态与默认勾选。
func TestClassifyThreeStates(t *testing.T) {
	root := t.TempDir()
	archive := newArchive(t,
		archiveEntry{path: "a/same.txt", text: "[same]\n\t1\n"},
		archiveEntry{path: "a/diff.txt", text: "[diff]\n\t1\n"},
	)
	same := archiveText(t, archive, "a/same.txt")
	diff := archiveText(t, archive, "a/diff.txt")

	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/same.txt", Dest: "a/same.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "a/diff.txt", Dest: "a/diff.txt", Form: "text", DataType: pvf.TypeScript},
		{Path: "a/new.txt", Dest: "a/new.txt", Form: "text", DataType: pvf.TypeScript},
	})
	writePayload(t, root, "pvf/a/same.txt", same)
	writePayload(t, root, "pvf/a/diff.txt", diff+"[added]\n\t2\n")
	writePayload(t, root, "pvf/a/new.txt", "[new]\n\t3\n")

	entries := mustEntries(t, archive, mustSource(t, root))
	want := []struct {
		state    FileState
		selected bool
	}{
		{FileUnchanged, false},
		{FileOverwrite, false},
		{FileNew, true},
	}
	for index, expected := range want {
		if entries[index].State != expected.state || entries[index].Selected != expected.selected {
			t.Fatalf("%s: state=%s selected=%v, want state=%s selected=%v",
				entries[index].ArchivePath, entries[index].State, entries[index].Selected,
				expected.state, expected.selected)
		}
	}
}

// TestClassifyInfersMissingDataType 覆盖判据 3：旧 manifest（dataType=0）按扩展名推断。
func TestClassifyInfersMissingDataType(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "string/name.str", Dest: "string/name.str", Form: "text"},
		{Path: "equipment/x.equ", Dest: "equipment/x.equ", Form: "text"},
	})
	writePayload(t, root, "pvf/string/name.str", "hello")
	writePayload(t, root, "pvf/equipment/x.equ", "[x]\n\t1\n")

	archive := newArchive(t, archiveEntry{path: "unrelated/keep.txt", text: "[keep]\n\t1\n"})
	entries := mustEntries(t, archive, mustSource(t, root))
	if entries[0].DataType != pvf.TypeUnicode {
		t.Fatalf(".str 应推断为 TypeUnicode, got %d", entries[0].DataType)
	}
	if entries[1].DataType != pvf.TypeScript {
		t.Fatalf(".equ 应推断为 TypeScript, got %d", entries[1].DataType)
	}
}

// TestClassifyRejectsListFile 覆盖判据 4：.lst 不走文件通道，扩展名大小写不敏感。
func TestClassifyRejectsListFile(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "equipment/Equipment.LST", Dest: "equipment/Equipment.LST", Form: "text",
			DataType: pvf.TypeScript},
	})
	writePayload(t, root, "pvf/equipment/Equipment.LST", "1\t`x/y.equ`\n")

	archive := newArchive(t, archiveEntry{path: "unrelated/keep.txt", text: "[keep]\n\t1\n"})
	entries := mustEntries(t, archive, mustSource(t, root))
	if entries[0].State != FileUnsupported || entries[0].Selected {
		t.Fatalf(".lst 应为未勾选的 unsupported, got %+v", entries[0])
	}
	if !strings.Contains(entries[0].Reason, "登记表") {
		t.Fatalf(".lst 的 Reason 没有指向登记表: %q", entries[0].Reason)
	}
}

// TestFromExtractionUsesManifestPath 覆盖判据 5：目标路径以 manifest.Files[].path 为准，
// 不是磁盘相对路径，也不是 dest。
func TestFromExtractionUsesManifestPath(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/b/c.txt", Dest: "x/y.txt", Form: "text", DataType: pvf.TypeScript},
	})
	writePayload(t, root, "pvf/x/y.txt", "[x]\n\t1\n")

	source := mustSource(t, root)
	candidate := source.Candidates[0]
	if candidate.ArchivePath != "a/b/c.txt" {
		t.Fatalf("ArchivePath = %q, want a/b/c.txt", candidate.ArchivePath)
	}
	wantSource := filepath.Join(root, "pvf", "x", "y.txt")
	if candidate.SourcePath != wantSource {
		t.Fatalf("SourcePath = %q, want %q", candidate.SourcePath, wantSource)
	}

	// 归档里只有磁盘布局的 x/y.txt。若实现误把磁盘相对路径当目标路径，这里会命中；
	// 正确实现查的是 a/b/c.txt，因此必须是 new。
	archive := newArchive(t, archiveEntry{path: "x/y.txt", text: "[x]\n\t1\n"})
	entries := mustEntries(t, archive, source)
	if entries[0].State != FileNew {
		t.Fatalf("应按 manifest 路径判 new, got %s", entries[0].State)
	}
}

// TestClassifyMissingPayloadIsInvalid 覆盖判据 6：载荷缺失只废这一条，不失败整批。
func TestClassifyMissingPayloadIsInvalid(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/missing.txt", Dest: "a/missing.txt", Form: "text", DataType: pvf.TypeScript},
	})

	// FromExtraction 只解析路径，不能因为文件不存在就报错：缺失是计划阶段的信息。
	source := mustSource(t, root)
	entries := mustEntries(t, newArchive(t), source)
	if entries[0].State != FileInvalid || entries[0].Selected {
		t.Fatalf("缺失载荷应为未勾选的 invalid, got %+v", entries[0])
	}
	if !strings.Contains(entries[0].Reason, "读取源文件失败") {
		t.Fatalf("invalid 的 Reason 没有带上错误: %q", entries[0].Reason)
	}
}

// TestFromExtractionRequiresManifest 覆盖判据 7：普通目录不是提取产物。
func TestFromExtractionRequiresManifest(t *testing.T) {
	if _, err := FromExtraction(t.TempDir()); !errors.Is(err, extraction.ErrNoManifest) {
		t.Fatalf("期望 ErrNoManifest, got %v", err)
	}
}

// TestClassifyComparesAgainstActiveText 覆盖判据 8：比较基准是 a.Text，不是 CanonicalText。
// 这里用自定义渲染规则让两者真正不同；若实现退回 CanonicalText，同内容会被判成覆盖。
func TestClassifyComparesAgainstActiveText(t *testing.T) {
	archive := newArchive(t,
		archiveEntry{path: "equipment/test.equ", text: "[records]\n1 2 3 4 5\n[/records]"},
	)
	engine, err := rendering.Parse([]byte(`{
  "version": 1,
  "rules": [{
    "id": "equ.records",
    "match": {"extensions": [".equ"]},
    "target": {"kind": "section", "section": "records"},
    "format": {"tokensPerLine": 2}
  }]
}`))
	if err != nil {
		t.Fatalf("rendering.Parse: %v", err)
	}
	archive.SetScriptRenderer(engine)

	index, ok := archive.Find("equipment/test.equ")
	if !ok {
		t.Fatal("归档缺少 equipment/test.equ")
	}
	active, err := archive.Text(index)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	canonical, err := archive.CanonicalText(index)
	if err != nil {
		t.Fatalf("CanonicalText: %v", err)
	}
	if active == canonical {
		t.Fatalf("测试前提不成立：渲染覆盖没有改变文本 %q", active)
	}

	root := t.TempDir()
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "equipment/test.equ", Dest: "equipment/test.equ", Form: "text",
			DataType: pvf.TypeScript},
	})
	// 提取侧写盘的是 a.Text 的结果，所以磁盘内容就是 active。
	writePayload(t, root, "pvf/equipment/test.equ", active)

	entries := mustEntries(t, archive, mustSource(t, root))
	if entries[0].State != FileUnchanged {
		t.Fatalf("同内容应为 unchanged, got %s（可能用了 CanonicalText 比较）", entries[0].State)
	}
}

// TestClassifyDataTypeDiffForcesOverwrite 覆盖规则 6 的第一分支：类型不同即覆盖，
// 即使文本完全一样。
func TestClassifyDataTypeDiffForcesOverwrite(t *testing.T) {
	root := t.TempDir()
	archive := newArchive(t, archiveEntry{path: "a/typed.txt", text: "hello", dataType: pvf.TypeUnicode})
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/typed.txt", Dest: "a/typed.txt", Form: "text", DataType: pvf.TypeScript},
	})
	writePayload(t, root, "pvf/a/typed.txt", "hello")

	entries := mustEntries(t, archive, mustSource(t, root))
	if entries[0].State != FileOverwrite || entries[0].Selected {
		t.Fatalf("类型不同应为未勾选的 overwrite, got %+v", entries[0])
	}
	if entries[0].Reason != "类型不同" {
		t.Fatalf("Reason = %q, want 类型不同", entries[0].Reason)
	}
}

// TestClassifyStripsOneLeadingBOM 与 services/import.go:522 的口径对齐：
// 带一个 BOM 的同内容文件仍是 unchanged。
func TestClassifyStripsOneLeadingBOM(t *testing.T) {
	root := t.TempDir()
	archive := newArchive(t, archiveEntry{path: "a/bom.txt", text: "[bom]\n\t1\n"})
	text := archiveText(t, archive, "a/bom.txt")
	writeManifest(t, root, []extraction.ExportedFile{
		{Path: "a/bom.txt", Dest: "a/bom.txt", Form: "text", DataType: pvf.TypeScript},
	})
	writePayload(t, root, "pvf/a/bom.txt", "\uFEFF"+text)

	entries := mustEntries(t, archive, mustSource(t, root))
	if entries[0].State != FileUnchanged {
		t.Fatalf("带一个 BOM 的同内容应为 unchanged, got %s", entries[0].State)
	}
}

// TestFromExtractionRejectsEmptyManifest 保证空包不会静默通过。
func TestFromExtractionRejectsEmptyManifest(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, nil)
	_, err := FromExtraction(root)
	if err == nil || !strings.Contains(err.Error(), "manifest 没有任何条目") {
		t.Fatalf("空 manifest 应报错, got %v", err)
	}
}
