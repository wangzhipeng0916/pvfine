package extraction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pvfine/internal/pvf"
)

func writeManifestFile(t *testing.T, root string, manifest Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("编码 manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0o644); err != nil {
		t.Fatalf("写入 manifest: %v", err)
	}
}

func TestReadManifestMissing(t *testing.T) {
	_, err := ReadManifest(t.TempDir())
	if !errors.Is(err, ErrNoManifest) {
		t.Fatalf("期望 ErrNoManifest, got %v", err)
	}
}

func TestReadManifestRejectsUnknownSchema(t *testing.T) {
	root := t.TempDir()
	writeManifestFile(t, root, Manifest{SchemaVersion: SemanticVersion + 1})
	if _, err := ReadManifest(root); err == nil {
		t.Fatal("未知 schema 版本必须拒绝")
	}
}

// TestReadManifestDefaultsDestAndKeepsDataType pins the two fields an importer
// cannot derive: Dest (where the payload actually is) and DataType (how to
// encode it back). A manifest written before DataType existed must still load.
func TestReadManifestDefaultsDestAndKeepsDataType(t *testing.T) {
	root := t.TempDir()
	writeManifestFile(t, root, Manifest{
		SchemaVersion: SemanticVersion,
		Files: []ExportedFile{
			{Path: "monster/goblin/goblin.mob", Form: FormText, DataType: pvf.TypeScript},
			{Path: "string/name.str", Dest: "string/name.str", Form: FormText, DataType: pvf.TypeUnicode},
			{Path: "old/legacy.etc", Form: FormText}, // 旧包：没有 Dest、没有 DataType
		},
	})
	manifest, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got := manifest.Files[2].Dest; got != "old/legacy.etc" {
		t.Fatalf("空 dest 应回落到 path（两者同基准，都相对 manifest.subdir）, got %q", got)
	}
	if got := manifest.Files[0].DataType; got != pvf.TypeScript {
		t.Fatalf("script 类型丢失: %d", got)
	}
	if got := manifest.Files[1].DataType; got != pvf.TypeUnicode {
		t.Fatalf("unicode 类型丢失: %d", got)
	}
	if got := manifest.Files[2].DataType; got != 0 {
		t.Fatalf("旧包缺失 dataType 应为 0, got %d", got)
	}
}

func TestReadManifestRejectsBadPaths(t *testing.T) {
	cases := map[string]string{
		"空路径":  "",
		"上跳":   "../evil.txt",
		"绝对":   "/etc/passwd",
		"空片段":  "a//b.txt",
		"反斜杠":  `a\b.txt`,
		"盘符":   "C:/a.txt",
		"点片段":  "a/./b.txt",
		"首尾空白": " a.txt",
		"控制字符": "a\x00b.txt",
	}
	for name, badPath := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeManifestFile(t, root, Manifest{
				SchemaVersion: SemanticVersion,
				Files:         []ExportedFile{{Path: badPath, Form: FormText}},
			})
			if _, err := ReadManifest(root); err == nil {
				t.Fatalf("非法 path %q 必须拒绝", badPath)
			}
		})
	}
}

func TestManifestDestPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	manifest := &Manifest{SchemaVersion: SemanticVersion}
	if _, err := manifest.DestPath(root, ExportedFile{Path: "a.txt", Dest: "../a.txt"}); err == nil {
		t.Fatal("dest 越界必须拒绝")
	}
	got, err := manifest.DestPath(root, ExportedFile{Path: "a.txt", Dest: "a.txt"})
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join(root, "pvf", "a.txt")
	gotAbs, _ := filepath.Abs(got)
	wantAbs, _ := filepath.Abs(want)
	if gotAbs != wantAbs {
		t.Fatalf("DestPath = %q, want %q", gotAbs, wantAbs)
	}
}

// TestExportManifestRecordsDataType is the contract the importer relies on:
// every exported entry carries its archive content type, so a file whose name
// does not reveal the type can still be written back correctly.
func TestExportManifestRecordsDataType(t *testing.T) {
	fixture := newFixture(t)
	scanner := fixture.scanner(t)
	seed, ok := fixture.archive.Find("map/forest/a.map")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}
	closure, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	root := t.TempDir()
	if _, err := Export(fixture.archive, closure, ExportOptions{
		Dir: root, WithReports: true, Form: FormText, Scanner: scanner,
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	manifest, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("manifest 没有条目")
	}
	for _, file := range manifest.Files {
		if file.DataType == 0 {
			t.Fatalf("%s 没有记录 dataType", file.Path)
		}
		index, ok := fixture.archive.Find(file.Path)
		if !ok {
			t.Fatalf("manifest 引用了不存在的条目 %s", file.Path)
		}
		if want := fixture.archive.File(index).DataType; file.DataType != want {
			t.Fatalf("%s dataType = %d, want %d", file.Path, file.DataType, want)
		}
	}
}
