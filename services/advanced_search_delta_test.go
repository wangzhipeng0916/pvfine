package services

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pvfine/internal/pvf"
)

func TestAdvancedDeltaPreservesShardsAndMatchesFullRebuild(t *testing.T) {
	c := NewCore()
	a := pvf.New()
	mustAddText(t, a, "dir/first.equ", "[name]\n`common first`", pvf.TypeScript)
	mustAddText(t, a, "dir/second.equ", "[name]\n`common second`", pvf.TypeScript)
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	svc := NewArchiveService(c)
	initial, err := svc.AdvancedSearch("string", "common", "", false, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := c.advancedDisk
	dir := d.dir
	shards := make(map[string][32]byte)
	for shard := 0; shard < d.shards; shard++ {
		name := advancedShardName(shard)
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		shards[name] = sha256.Sum256(raw)
	}
	if err := NewEditorService(c).SetText(0, "[name]\n`common changed`"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvancedSearch("string", "common", "", false, initial.NextCursor, 1); !errors.Is(err, ErrAdvancedCursorStale) {
		t.Fatalf("old cursor accepted: %v", err)
	}
	if _, err := svc.AdvancedSearch("string", "changed", "", false, 0, 10); err != nil {
		t.Fatal(err)
	}
	if c.advancedDisk != d || d.dir != dir || !d.overlay {
		t.Fatal("editing rebuilt the reference base")
	}
	privateDB := d.db
	created, err := svc.CreateFile("dir/new.equ", pvf.TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewEditorService(c).SetText(created.FileIndex, "[name]\n`common new`"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	actual, err := svc.AdvancedSearch("string", "common", "", false, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	snapshot := c.archive.CloneForBatch()
	c.mu.RUnlock()
	oracle := NewCore()
	if err := oracle.setArchive(snapshot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oracle.closeArchive)
	expected, err := NewArchiveService(oracle).AdvancedSearch("string", "common", "", false, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Hits, expected.Hits) {
		t.Fatalf("incremental references differ from rebuild\nactual=%#v\nexpected=%#v", actual.Hits, expected.Hits)
	}
	if c.advancedDisk != d || d.db != privateDB {
		t.Fatal("later edits replaced the private projection")
	}
	for name, before := range shards {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || sha256.Sum256(raw) != before {
			t.Fatalf("immutable shard changed: %s err=%v", name, err)
		}
	}
}

func TestAdvancedDeltaRollbackCanRetry(t *testing.T) {
	c, svc := advancedFixture(t)
	t.Cleanup(c.closeArchive)
	if _, err := svc.AdvancedSearch("string", "共同", "", false, 0, 20); err != nil {
		t.Fatal(err)
	}
	d := c.advancedDisk
	d.mu.Lock()
	if err := d.prepareOverlay(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`CREATE TRIGGER fail_delta BEFORE INSERT ON delta_refs BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	if err := NewEditorService(c).SetText(0, "[name]\n`replacement`"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvancedSearch("string", "replacement", "", false, 0, 20); err == nil {
		t.Fatal("failed reference delta reported success")
	}
	d.mu.Lock()
	if _, err := d.db.Exec(`DROP TRIGGER fail_delta`); err != nil {
		t.Fatal(err)
	}
	d.mu.Unlock()
	result, err := svc.AdvancedSearch("string", "replacement", "", false, 0, 20)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].FileIndex != 0 {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
}

func TestAdvancedDeltaSavedOverlayCacheIsImmutableAndReusable(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("LocalAppData", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("HOME", cacheRoot)
	c := NewCore()
	a := pvf.New()
	mustAddText(t, a, "dir/first.equ", "[name]\n`baseline cached value`", pvf.TypeScript)
	path := filepath.Join(t.TempDir(), "overlay.pvf")
	if err := a.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	svc := NewArchiveService(c)
	if _, err := svc.AdvancedSearch("string", "baseline", "", false, 0, 20); err != nil {
		t.Fatal(err)
	}
	if err := NewEditorService(c).SetText(0, "[name]\n`overlay cached value`"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvancedSearch("string", "overlay", "", false, 0, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEditorService(c).Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvancedSearch("string", "overlay", "", false, 0, 20); err != nil {
		t.Fatal(err)
	}
	cachePath := c.advancedDisk.cachePath
	if cachePath == "" {
		t.Fatal("saved overlay was not cached")
	}
	cacheBytes, err := os.ReadFile(filepath.Join(cachePath, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(cacheBytes)
	c.closeArchive()
	if _, err := svc.Open(path); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.AdvancedSearch("string", "overlay", "", false, 0, 20); err != nil || len(result.Hits) != 1 {
		t.Fatalf("cached search=%#v err=%v", result, err)
	}
	if svc.AdvancedIndexStatus().Stage != "ready-cache" || !c.advancedDisk.overlay {
		t.Fatal("saved overlay cache was not restored")
	}
	if err := NewEditorService(c).SetText(0, "[name]\n`after cached overlay`"); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.AdvancedSearch("string", "after cached", "", false, 0, 20); err != nil || len(result.Hits) != 1 {
		t.Fatalf("cached overlay edit=%#v err=%v", result, err)
	}
	cacheBytes, err = os.ReadFile(filepath.Join(cachePath, "index.db"))
	if err != nil || sha256.Sum256(cacheBytes) != before {
		t.Fatalf("editing changed immutable cache: %v", err)
	}
}
