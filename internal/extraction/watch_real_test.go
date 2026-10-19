package extraction_test

import (
	"testing"
	"time"

	"pvfine/internal/extraction"
)

// TestWatchEntriesCostOnRealArchive measures what the preview witness adds to
// the interactive path, because the point of a witness is that it must stay
// cheaper than the scan it protects.
func TestWatchEntriesCostOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	started := time.Now()
	if err := scanner.Prewarm(nil); err != nil {
		t.Fatalf("预热失败: %v", err)
	}
	t.Logf("预热耗时=%s", time.Since(started).Round(time.Millisecond))

	lists := scanner.RegistrySet().ListFiles()
	indexes := make([]int32, 0, len(lists))
	for _, listPath := range lists {
		if index, ok := archive.Find(listPath); ok {
			indexes = append(indexes, index)
		}
	}
	started = time.Now()
	registry := extraction.WatchEntries(archive, indexes)
	t.Logf("登记表指纹：声明 %d 张表 / 命中 %d 条目 %.2f MB / %s",
		len(lists), registry.Entries, float64(registry.Bytes)/1e6,
		time.Since(started).Round(time.Millisecond))
	if registry.Entries == 0 {
		t.Fatal("一张登记表都没命中，指纹无法保护解析结果")
	}

	seed, ok := archive.Find(realDungeonSeed)
	if !ok {
		t.Skipf("归档不含 %s", realDungeonSeed)
	}
	closure, err := scanner.Closure([]int32{seed}, extraction.Options{CollectUnmodeled: true})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	watchSet := make([]int32, 0, len(closure.Nodes)+len(indexes))
	for _, node := range closure.Nodes {
		watchSet = append(watchSet, node.Index)
	}
	watchSet = append(watchSet, indexes...)

	started = time.Now()
	withClosure := extraction.WatchEntries(archive, watchSet)
	closureOnly := time.Since(started)
	again := extraction.WatchEntries(archive, watchSet)
	if again != withClosure {
		t.Fatal("同一内容两次取证结果不一致")
	}
	t.Logf("闭包 %d 条目：指纹 %.2f MB 用 %s；登记表单独 %d 条目",
		len(closure.Nodes), float64(withClosure.Bytes)/1e6,
		closureOnly.Round(time.Millisecond), registry.Entries)
	if withClosure.FileCount != archive.FileCount() {
		t.Errorf("指纹里的条目总数 = %d, want %d", withClosure.FileCount, archive.FileCount())
	}
}
