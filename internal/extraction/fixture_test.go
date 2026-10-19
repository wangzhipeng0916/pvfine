package extraction

import (
	"path/filepath"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// entry is one synthetic archive member.
type entry struct {
	path string
	text string
}

// fixture is a tiny PVF archive written to a temp file and reopened, because a
// store binds to an archive identity that requires a real source path.
type fixture struct {
	archive *pvf.Archive
	specs   map[string]annotations.RelationSpec
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureFrom(t, nil, testDocument())
}

func newFixtureFrom(t *testing.T, extra []entry, doc Document) *fixture {
	t.Helper()
	dir := t.TempDir()
	created := pvf.New()
	add := func(path, text string) {
		t.Helper()
		if _, err := created.AddFileText(path, text, pvf.TypeScript); err != nil {
			t.Fatalf("AddFileText(%s): %v", path, err)
		}
	}
	add("map/map.lst", "1\t`forest/a.map`\n2\t`forest/b.map`\n")
	add("passiveobject/passiveobject.lst", "10\t`obj/door.obj`\n11\t`obj/crate.obj`\n")
	// 反编译 PVF 每个字段独占一行，行首为制表符；fixture 必须复刻该形状。
	add("map/forest/a.map", "[dungeon]\n\t1\n[passive object]\n\t10\n\t1\n\t2\n\t3\n"+
		"\t11\n\t4\n\t5\n\t6\n[tile]\n\t`tiles/ground.til`\n")
	add("map/forest/b.map", "[dungeon]\n\t1\n[passive object]\n\t10\n\t0\n\t0\n\t0\n")
	add("map/forest/tiles/ground.til", "[image]\n\t`tiles/ground.img`\n\t0\n")
	add("passiveobject/obj/door.obj", "[use animation]\n\t`door.ani`\n[mystery anim]\n\t`hidden.ani`\n[int data]\n\t1000\n\t2000\n")
	add("passiveobject/obj/crate.obj", "[use animation]\n\t`door.ani`\n")
	add("passiveobject/obj/door.ani", "[FRAME MAX]\n\t2\n[IMAGE]\n\t``\n\t0\n")
	add("passiveobject/obj/hidden.ani", "[FRAME MAX]\n\t1\n")
	for _, member := range extra {
		add(member.path, member.text)
	}

	file := filepath.Join(dir, "Script.pvf")
	if err := created.SaveAs(file); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return &fixture{archive: archive, specs: map[string]annotations.RelationSpec{
		"map":  {ListPath: "map/map.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"被动对象": {ListPath: "passiveobject/passiveobject.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
	}}
}

func (f *fixture) scanner(t *testing.T) *Scanner {
	t.Helper()
	return f.scannerWith(t, testDocument())
}

// registrySet builds the tables for the fixture's current specs, so seed
// resolution can be tested against the same registry the scanner resolves with.
func (f *fixture) registrySet() *RegistrySet {
	return NewRegistrySet(f.archive, f.specs)
}

func (f *fixture) scannerWith(t *testing.T, doc Document) *Scanner {
	t.Helper()
	catalog, err := NewCatalog(doc)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return NewScanner(f.archive, catalog, NewRegistrySet(f.archive, f.specs))
}

// scanNode runs one entry through the scanner and returns its references.
func (f *fixture) scanNode(t *testing.T, scanner *Scanner, filePath string) []Ref {
	t.Helper()
	return f.scanFile(t, scanner, filePath).Refs
}

// scanFile runs one entry through the scanner and returns the whole result.
func (f *fixture) scanFile(t *testing.T, scanner *Scanner, filePath string) ScanResult {
	t.Helper()
	index, ok := f.archive.Find(filePath)
	if !ok {
		t.Fatalf("fixture 缺少 %s", filePath)
	}
	result, err := scanner.ScanFile(index)
	if err != nil {
		t.Fatalf("ScanFile(%s): %v", filePath, err)
	}
	return result
}

func testDocument() Document {
	return Document{Version: 1, Edges: []Edge{
		{ID: "map.passiveObject", Extensions: []string{".map"}, Section: "passive object",
			TokenTypes: []int32{0}, Record: &Record{Width: 4, RefOffsets: []int{0}},
			Target: Target{Kind: TargetRegistry, Relation: "被动对象"}},
		{ID: "map.tile", Extensions: []string{".map"}, Section: "tile",
			TokenTypes: []int32{6}, Target: Target{Kind: TargetEntry, ExpectExt: []string{".til"}}},
		{ID: "action.useAnimation", Extensions: []string{".obj", ".als"}, Section: "use animation",
			TokenTypes: []int32{6}, Target: Target{Kind: TargetEntry}},
		{ID: "til.image", Extensions: []string{".til"}, Section: "image",
			TokenTypes: []int32{6}, Target: Target{Kind: TargetAsset}},
	}}
}
