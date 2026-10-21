package extraction_test

import (
	"os"
	"path/filepath"
	"testing"

	"pvfine/internal/extraction"
	"pvfine/internal/importing"
	"pvfine/internal/pvf"
)

// TestExtractionProductIsImportable is the contract between the two halves:
// whatever Export writes, the import centre must be able to read back — same
// paths, same payload locations, same subdir convention.
//
// It exists because unit tests that hand-write the manifest can silently agree
// with the reader on a wrong convention (Dest is relative to the payload
// subdir, not to the export root), which is exactly the bug this catches.
func TestExtractionProductIsImportable(t *testing.T) {
	created := pvf.New()
	for path, text := range map[string]string{
		"dungeon/act1/grakkarak.dgn": "[dungeon]\n\t1\n",
		"monster/goblin/goblin.mob":  "[monster]\n\t1\n",
		"passiveobject/obj/door.obj": "[use animation]\n\t`door.ani`\n",
		"passiveobject/obj/door.ani": "[FRAME MAX]\n\t1\n",
		"string/name.str":            "name>测试\n",
	} {
		dataType := pvf.TypeScript
		if filepath.Ext(path) == ".str" {
			dataType = pvf.TypeUnicode
		}
		if _, err := created.AddFileText(path, text, dataType); err != nil {
			t.Fatalf("AddFileText(%s): %v", path, err)
		}
	}
	archivePath := filepath.Join(t.TempDir(), "Script.pvf")
	if err := created.SaveAs(archivePath); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	archive, err := pvf.Open(archivePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seed, ok := archive.Find("dungeon/act1/grakkarak.dgn")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}

	catalog, err := extraction.NewCatalog(extraction.Document{Version: 1, Edges: []extraction.Edge{
		{ID: "obj.animation", Extensions: []string{".obj"}, Section: "use animation",
			TokenTypes: []int32{6}, Target: extraction.Target{Kind: extraction.TargetEntry}},
	}})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	scanner := extraction.NewScanner(archive, catalog, extraction.NewRegistrySet(archive, nil))
	closure, err := scanner.Closure([]int32{seed}, extraction.Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}

	root := t.TempDir()
	if _, err := extraction.Export(archive, closure, extraction.ExportOptions{
		Dir: root, WithReports: true, Form: extraction.FormText, Scanner: scanner,
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	manifest, err := extraction.ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Subdir != extraction.DefaultSubdir {
		t.Fatalf("manifest.subdir = %q, want %q", manifest.Subdir, extraction.DefaultSubdir)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("manifest 没有条目")
	}
	for _, file := range manifest.Files {
		path, err := manifest.DestPath(root, file)
		if err != nil {
			t.Fatalf("DestPath(%s): %v", file.Path, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("载荷找不到: %s -> %s: %v", file.Path, path, err)
		}
	}

	source, err := importing.FromExtraction(root)
	if err != nil {
		t.Fatalf("FromExtraction: %v", err)
	}
	entries, err := importing.Classify(archive, source)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(entries) != len(manifest.Files) {
		t.Fatalf("条目数不一致: %d vs %d", len(entries), len(manifest.Files))
	}
	for _, entry := range entries {
		if entry.State == importing.FileInvalid {
			t.Fatalf("%s 被判为 invalid: %s", entry.ArchivePath, entry.Reason)
		}
		// 归档没变，原样导出再导回应当全部"未变"。
		if entry.State != importing.FileUnchanged {
			t.Fatalf("%s 应为 unchanged, got %s (%s)", entry.ArchivePath, entry.State, entry.Reason)
		}
	}
}
