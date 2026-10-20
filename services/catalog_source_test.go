package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pvfine/internal/appdirs"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

func writeCatalogFile(t *testing.T, path string, data []byte, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// mtime 是热加载的判据；显式设置才能不依赖文件系统的写入时间粒度。
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func marshalCatalog(t *testing.T, doc extraction.Document) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func builtinCatalogDocument(t *testing.T) extraction.Document {
	t.Helper()
	var doc extraction.Document
	if err := json.Unmarshal(extraction.EmbeddedEdges, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func findEdge(doc extraction.Document, id string) *extraction.Edge {
	for i := range doc.Edges {
		if strings.EqualFold(doc.Edges[i].ID, id) {
			return &doc.Edges[i]
		}
	}
	return nil
}

func TestCatalogSourceMergesExternalCatalogByID(t *testing.T) {
	builtin := builtinCatalogDocument(t)
	path := filepath.Join(t.TempDir(), extractionCatalogFile)
	override := builtin.Edges[0]
	override.Note = "external override"
	external := extraction.Document{
		Version: builtin.Version,
		Categories: []extraction.Category{
			{ID: "testcat", Name: "测试类别", Extensions: []string{".zzz"}},
		},
		Edges: []extraction.Edge{
			override,
			{
				ID: "test.extra", Extensions: []string{".zzz"}, Section: "test section",
				Target: extraction.Target{Kind: extraction.TargetEntry, Roots: []string{"test"}},
			},
		},
	}
	writeCatalogFile(t, path, marshalCatalog(t, external), time.Now())

	source := &catalogSource{path: path}
	snapshot, err := source.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snapshot.status.Source != "external" {
		t.Fatalf("来源应为 external, got %q", snapshot.status.Source)
	}
	if snapshot.key == "builtin" {
		t.Fatal("外部目录生效后 key 不应再是 builtin")
	}
	if want := len(builtin.Edges) + 1; snapshot.status.EdgeCount != want {
		t.Errorf("合集边数=%d, want %d", snapshot.status.EdgeCount, want)
	}
	if snapshot.status.ExternalEdgeCount != 2 {
		t.Errorf("外部声明边数=%d, want 2", snapshot.status.ExternalEdgeCount)
	}
	merged := snapshot.catalog.Document()
	if got := findEdge(merged, override.ID); got == nil || got.Note != "external override" {
		t.Errorf("同名边应被外部声明覆盖, got %+v", got)
	}
	if got := findEdge(merged, "test.extra"); got == nil {
		t.Error("外部新增的边应进入合集目录")
	}
	// 外部没提到的内置边必须保留，否则升级补的边到不了带覆盖文件的用户手里。
	if len(merged.Edges) != len(builtin.Edges)+1 {
		t.Errorf("合集边数=%d, 内置=%d", len(merged.Edges), len(builtin.Edges))
	}
	if !snapshot.catalog.HasCategory("testcat") {
		t.Error("外部新增类别应可用")
	}
	for _, category := range builtin.Categories {
		if !snapshot.catalog.HasCategory(category.ID) {
			t.Errorf("内置类别 %s 不应因外部目录消失", category.ID)
		}
	}
}

func TestCatalogSourceFallsBackToBuiltinWithoutFile(t *testing.T) {
	source := &catalogSource{path: filepath.Join(t.TempDir(), extractionCatalogFile)}
	snapshot, err := source.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snapshot.status.Source != "builtin" || snapshot.key != "builtin" {
		t.Fatalf("无外部文件时应使用内置目录, got %+v key=%q", snapshot.status, snapshot.key)
	}
	if want := len(builtinCatalogDocument(t).Edges); snapshot.status.EdgeCount != want {
		t.Errorf("内置边数=%d, want %d", snapshot.status.EdgeCount, want)
	}
	if snapshot.status.Warning != "" {
		t.Errorf("没有外部文件不应报警, got %q", snapshot.status.Warning)
	}
}

func TestCatalogSourceKeepsLastGoodOnBrokenFile(t *testing.T) {
	builtin := builtinCatalogDocument(t)
	path := filepath.Join(t.TempDir(), extractionCatalogFile)
	source := &catalogSource{path: path}
	base := time.Now()
	extraEdge := func(id string) extraction.Edge {
		return extraction.Edge{
			ID: id, Extensions: []string{".zzz"}, Section: "test section",
			Target: extraction.Target{Kind: extraction.TargetEntry, Roots: []string{"test"}},
		}
	}

	writeCatalogFile(t, path, marshalCatalog(t, extraction.Document{
		Version: builtin.Version, Edges: []extraction.Edge{extraEdge("test.one")},
	}), base)
	first, err := source.Load()
	if err != nil {
		t.Fatalf("Load(valid): %v", err)
	}
	if first.status.Source != "external" {
		t.Fatalf("来源应为 external, got %q", first.status.Source)
	}

	writeCatalogFile(t, path, []byte("{ 这不是 JSON"), base.Add(2*time.Second))
	broken, err := source.Load()
	if err != nil {
		t.Fatalf("Load(broken): %v", err)
	}
	if broken.key != first.key || broken.status.EdgeCount != first.status.EdgeCount {
		t.Errorf("坏文件不应换掉上一份可用目录: key %q -> %q, 边数 %d -> %d",
			first.key, broken.key, first.status.EdgeCount, broken.status.EdgeCount)
	}
	if !strings.Contains(broken.status.Warning, "解析外部边目录失败") {
		t.Errorf("应说明坏文件原因, got %q", broken.status.Warning)
	}
	if !strings.Contains(broken.status.Warning, path) {
		t.Errorf("警告里要带文件路径，否则不知道去修哪个文件, got %q", broken.status.Warning)
	}

	writeCatalogFile(t, path, marshalCatalog(t, extraction.Document{
		Version: builtin.Version, Edges: []extraction.Edge{extraEdge("test.one"), extraEdge("test.two")},
	}), base.Add(4*time.Second))
	fixed, err := source.Load()
	if err != nil {
		t.Fatalf("Load(fixed): %v", err)
	}
	if fixed.status.Source != "external" || fixed.status.Warning != "" {
		t.Errorf("修好后应生效且清掉警告, got %+v", fixed.status)
	}
	if fixed.status.EdgeCount != len(builtin.Edges)+2 {
		t.Errorf("修好后边数=%d, want %d", fixed.status.EdgeCount, len(builtin.Edges)+2)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed, err := source.Load()
	if err != nil {
		t.Fatalf("Load(removed): %v", err)
	}
	if removed.status.Source != "builtin" || removed.status.EdgeCount != len(builtin.Edges) {
		t.Errorf("删除外部文件后应回到内置目录, got %+v", removed.status)
	}
}

func TestCatalogSourceRejectsBadExternalDeclarations(t *testing.T) {
	builtin := builtinCatalogDocument(t)
	validEdge := extraction.Edge{
		ID: "test.dup", Extensions: []string{".zzz"}, Section: "test section",
		Target: extraction.Target{Kind: extraction.TargetEntry, Roots: []string{"test"}},
	}
	cases := []struct {
		name string
		doc  extraction.Document
		want string
	}{
		{
			name: "重复边 id",
			doc: extraction.Document{Version: builtin.Version,
				Edges: []extraction.Edge{validEdge, validEdge}},
			want: "边 id 重复",
		},
		{
			name: "版本不符",
			doc: extraction.Document{Version: builtin.Version + 1,
				Edges: []extraction.Edge{validEdge}},
			want: "版本",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), extractionCatalogFile)
			writeCatalogFile(t, path, marshalCatalog(t, tc.doc), time.Now())
			source := &catalogSource{path: path}
			snapshot, err := source.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if snapshot.status.Source != "builtin" {
				t.Errorf("坏声明应退回内置目录, got %q", snapshot.status.Source)
			}
			if !strings.Contains(snapshot.status.Warning, tc.want) {
				t.Errorf("警告应包含 %q, got %q", tc.want, snapshot.status.Warning)
			}
		})
	}
}

// TestExternalCatalogReloadsWithoutRestart is the behavior the feature exists
// for: adding an edge in pvfine-data/extract-edges.json grows the closure on the
// next preview, with no restart and no archive change.
func TestExternalCatalogReloadsWithoutRestart(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(appdirs.DataDirEnv, dataDir)
	previous := extractionCatalog
	extractionCatalog = &catalogSource{}
	t.Cleanup(func() { extractionCatalog = previous })

	a := pvf.New()
	for path, text := range map[string]string{
		"test/x.flt":                      "[refs]\n\t`passiveobject/obj/door.obj`\n",
		"passiveobject/obj/door.obj":      "[use animation]\n\t`door.ani`\n",
		"passiveobject/obj/door.ani":      "[FRAME MAX]\n\t1\n",
		"passiveobject/passiveobject.lst": "10\t`obj/door.obj`\n",
	} {
		if _, err := a.AddFileText(path, text, pvf.TypeScript); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "test.pvf")
	if err := a.SaveAs(src); err != nil {
		t.Fatal(err)
	}
	opened, err := pvf.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	if err := c.setArchive(opened); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	svc := NewExtractionService(c)

	before, err := svc.Prepare(ExtractionRequest{Seeds: []string{"test/x.flt"}})
	if err != nil {
		t.Fatalf("Prepare(before): %v", err)
	}
	if before.ClosureCount != 1 {
		t.Fatalf("内置目录不认识 .flt [refs]，闭包应为 1, got %d", before.ClosureCount)
	}
	if status, err := svc.CatalogStatus(); err != nil || status.Source != "builtin" {
		t.Fatalf("初始应为内置目录, got %+v err=%v", status, err)
	}

	external := extraction.Document{
		Version: 1,
		Edges: []extraction.Edge{{
			ID: "test.flt.refs", Extensions: []string{".flt"}, Section: "refs",
			Target: extraction.Target{Kind: extraction.TargetEntry},
		}},
	}
	writeCatalogFile(t, filepath.Join(dataDir, extractionCatalogFile),
		marshalCatalog(t, external), time.Now().Add(time.Minute))

	after, err := svc.Prepare(ExtractionRequest{Seeds: []string{"test/x.flt"}})
	if err != nil {
		t.Fatalf("Prepare(after): %v", err)
	}
	if after.ClosureCount != 3 {
		t.Errorf("外部边生效后闭包应为 3（种子 + obj + ani）, got %d", after.ClosureCount)
	}
	if status, err := svc.CatalogStatus(); err != nil || status.Source != "external" {
		t.Errorf("应报告外部目录, got %+v err=%v", status, err)
	}
}
