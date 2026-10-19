package extraction

import (
	"path/filepath"
	"testing"

	"pvfine/internal/pvf"
)

func witnessArchive(t *testing.T) (*pvf.Archive, string) {
	t.Helper()
	a := pvf.New()
	for _, p := range []string{"list.lst", "dir/a.txt", "dir/b.txt", "other/c.txt"} {
		if _, err := a.AddFileText(p, "seed content for "+p, pvf.TypeUnicode); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "witness.pvf")
	if err := a.SaveAs(src); err != nil {
		t.Fatal(err)
	}
	return a, src
}

func indexOf(t *testing.T, a *pvf.Archive, p string) int32 {
	t.Helper()
	index, ok := a.Find(p)
	if !ok {
		t.Fatalf("条目 %s 不存在", p)
	}
	return index
}

func TestWatchEntriesIgnoresCollectionOrder(t *testing.T) {
	a, _ := witnessArchive(t)
	all := []int32{indexOf(t, a, "list.lst"), indexOf(t, a, "dir/a.txt"), indexOf(t, a, "dir/b.txt")}
	forward := WatchEntries(a, all)
	reversed := WatchEntries(a, []int32{all[2], all[1], all[0]})
	if forward != reversed {
		t.Fatalf("同一内容按不同收集顺序得到不同指纹: %+v vs %+v", forward, reversed)
	}
	withDuplicates := WatchEntries(a, []int32{all[0], all[0], all[1], all[2], -1, a.FileCount()})
	if forward != withDuplicates {
		t.Fatalf("重复或越界索引应被忽略: %+v vs %+v", forward, withDuplicates)
	}
	if forward.Entries != 3 {
		t.Errorf("Entries = %d, want 3", forward.Entries)
	}
}

// editedEntry is the content change a witness has to notice. kind picks where it
// lands: overlay only, or overlay followed by a save that empties both the
// journal and the overlay.
func TestWatchEntriesFollowsContentOfWatchedEntries(t *testing.T) {
	a, _ := witnessArchive(t)
	watched := []int32{indexOf(t, a, "list.lst"), indexOf(t, a, "dir/a.txt")}
	before := WatchEntries(a, watched)

	if err := a.SetText(indexOf(t, a, "dir/b.txt"), "changed but unwatched"); err != nil {
		t.Fatal(err)
	}
	if after := WatchEntries(a, watched); after != before {
		t.Fatalf("未纳入指纹的条目改动不应改变指纹: %+v vs %+v", after, before)
	}

	if err := a.SetText(indexOf(t, a, "dir/a.txt"), "changed and watched"); err != nil {
		t.Fatal(err)
	}
	if after := WatchEntries(a, watched); after == before {
		t.Fatal("纳入指纹的条目改了内容，指纹却没变")
	}
}

// TestWatchEntriesSurvivesJournalConsumption is the reason this witness exists:
// after a save the mutation journal and the pending-edit overlay are both empty,
// so no cheap archive signal is left to tell a preview that its content moved.
func TestWatchEntriesSurvivesJournalConsumption(t *testing.T) {
	a, src := witnessArchive(t)
	watched := []int32{indexOf(t, a, "list.lst"), indexOf(t, a, "dir/a.txt")}
	before := WatchEntries(a, watched)

	if err := a.SetText(indexOf(t, a, "dir/a.txt"), "rewritten content"); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a.ClearMutations()
	if a.MutationCheckpoint() != 0 {
		t.Fatalf("保存后仍留有变更日志: %d", a.MutationCheckpoint())
	}
	if len(a.PendingEdits()) != 0 {
		t.Fatalf("保存后仍报告待写入编辑: %v", a.PendingEdits())
	}

	if after := WatchEntries(a, watched); after == before {
		t.Fatal("编辑并已落盘，内容指纹却没反应")
	}
	if _, err := pvf.Open(src); err != nil {
		t.Fatalf("重新打开归档失败: %v", err)
	}
}

func TestWatchEntriesFollowsEntryCount(t *testing.T) {
	a, _ := witnessArchive(t)
	watched := []int32{indexOf(t, a, "list.lst")}
	before := WatchEntries(a, watched)
	added := a.AddFile("dir/d.txt", []byte("new entry"), pvf.TypeUnicode)
	if added < 0 {
		t.Fatal("AddFile 返回了非法索引")
	}
	// The new entry is deliberately not watched, yet an added file can change
	// which basename a missing reference resolves to, so the count joins the
	// digest.
	if after := WatchEntries(a, watched); after == before {
		t.Fatal("条目总数变了，内容指纹却没反应")
	}
}
