// Author: flobit
package services

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

func TestArchiveDiffStringPoolDifferenceAndMerge(t *testing.T) {
	// 创建基础归档 A
	dir := t.TempDir()
	pathA := filepath.Join(dir, "base.pvf")
	pathB := filepath.Join(dir, "other.pvf")

	a := pvf.New()
	// 写入脚本，引用 A 中的字符串
	if _, err := a.AddFileText("etc/test.etc", "[name]\n`hello_from_A`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFileText("etc/same.etc", "[name]\n`common_string`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAs(pathA); err != nil {
		t.Fatal(err)
	}

	// 创建对比归档 B，字符串池内容不同
	b := pvf.New()
	if _, err := b.AddFileText("etc/test.etc", "[name]\n`hello_from_B`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddFileText("etc/same.etc", "[name]\n`common_string`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddFileText("etc/added.etc", "[name]\n`newly_added`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAs(pathB); err != nil {
		t.Fatal(err)
	}

	core := NewCore()
	openA, err := pvf.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.setArchive(openA); err != nil {
		t.Fatal(err)
	}
	defer core.closeArchive()

	diffService := NewArchiveDiffService(core)
	status, err := diffService.Start(ArchiveDiffRequest{Path: pathB})
	if err != nil {
		t.Fatalf("Start diff failed: %v", err)
	}

	// 等待比较完成
	var finalStatus *ArchiveDiffStatus
	for i := 0; i < 50; i++ {
		s, err := diffService.Status(status.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == "ready" {
			finalStatus = s
			break
		}
		if s.State == "error" {
			t.Fatalf("diff failed with error: %s", s.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalStatus == nil {
		t.Fatal("diff timed out")
	}

	// 验证差异结果：
	// 默认方向下 other(B) 为 before，current(A) 为 after：
	// etc/test.etc 因字符串池不同识别为 Modified；
	// etc/added.etc 在 B 中存在而在 A 中不存在，在默认方向下标记为 Deleted；
	// etc/same.etc 内容相同，不出现在差异中。
	if finalStatus.Modified != 1 {
		t.Fatalf("Modified count = %d, want 1", finalStatus.Modified)
	}
	if finalStatus.Deleted != 1 {
		t.Fatalf("Deleted count = %d, want 1", finalStatus.Deleted)
	}

	// 验证 FlatFiles 全量与关键字过滤
	flatAll, err := diffService.FlatFiles(status.ID, "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(flatAll) != 2 {
		t.Fatalf("FlatFiles count = %d, want 2", len(flatAll))
	}
	flatFilteredTest, err := diffService.FlatFiles(status.ID, "test", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(flatFilteredTest) != 1 || flatFilteredTest[0].Path != "etc/test.etc" || flatFilteredTest[0].Name != "test.etc" {
		t.Fatalf("FlatFiles filtered by 'test' unexpected: %+v", flatFilteredTest)
	}
	flatNone, err := diffService.FlatFiles(status.ID, "nonexistent_query", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(flatNone) != 0 {
		t.Fatalf("FlatFiles filtered by nonexistent should be empty, got: %+v", flatNone)
	}

	// 验证 FilteredChildren
	allChildren, err := diffService.FilteredChildren(status.ID, "", "all")
	if err != nil || len(allChildren) != 1 || allChildren[0].Name != "etc" || allChildren[0].Count != 2 {
		t.Fatalf("FilteredChildren all failed: %+v", allChildren)
	}
	modChildren, err := diffService.FilteredChildren(status.ID, "", "modify")
	if err != nil || len(modChildren) != 1 || modChildren[0].Name != "etc" || modChildren[0].Count != 1 {
		t.Fatalf("FilteredChildren modify failed: %+v", modChildren)
	}
	delChildren, err := diffService.FilteredChildren(status.ID, "", "delete")
	if err != nil || len(delChildren) != 1 || delChildren[0].Name != "etc" || delChildren[0].Count != 1 {
		t.Fatalf("FilteredChildren delete failed: %+v", delChildren)
	}
	addChildren, err := diffService.FilteredChildren(status.ID, "", "add")
	if err != nil || len(addChildren) != 0 {
		t.Fatalf("FilteredChildren add should be empty, got: %+v", addChildren)
	}

	// 测试 MergeFiles：将 B 中的 etc/test.etc 合并到 A
	res, err := diffService.MergeFiles(status.ID, []string{"etc/test.etc"})
	if err != nil {
		t.Fatalf("MergeFiles failed: %v", err)
	}
	if res.SuccessCount != 1 || len(res.MergedPaths) != 1 {
		t.Fatalf("Merge result unexpected: %+v", res)
	}

	// 验证 A 中的内容已经被成功更新为 B 中的字符串
	idx, ok := core.archive.Find("etc/test.etc")
	if !ok {
		t.Fatal("etc/test.etc not found in archive A after merge")
	}
	mergedText, err := core.archive.CanonicalText(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mergedText, "hello_from_B") {
		t.Fatalf("merged text = %q, want hello_from_B", mergedText)
	}

	// 测试 MergeText：修改单文件文本（仍与 B 有差异）
	newContent := "[name]\n\t`manually_edited`\n"
	if err := diffService.MergeText(status.ID, "etc/test.etc", newContent); err != nil {
		t.Fatalf("MergeText failed: %v", err)
	}
	files, err := diffService.FlatFiles(status.ID, "test.etc", "modify")
	if err != nil || len(files) != 1 {
		t.Fatalf("re-edited merged file missing from diff: files=%+v err=%v", files, err)
	}
	detail, err := diffService.FileDiff(status.ID, "etc/test.etc")
	if err != nil || detail.Operation != "modify" || !strings.Contains(detail.AfterText, "manually_edited") {
		t.Fatalf("re-edited file detail: %+v err=%v", detail, err)
	}
	updated, err := diffService.Status(status.ID)
	if err != nil || updated.Modified != 1 {
		t.Fatalf("re-edited file statistics: %+v err=%v", updated, err)
	}
	exports := NewExportService(core)
	preview, err := exports.Prepare(ExportRequest{Source: "archive-diff", DiffID: status.ID, Mode: "direct", Scopes: []string{"etc/test.etc"}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := exports.executeTo(preview.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if text := readExportFile(t, root, "etc/test.etc"); !strings.Contains(text, "manually_edited") {
		t.Fatalf("export lost re-edited merged file: %q", text)
	}
	// 测试通过目录路径合并整个目录下的所有差异文件（包含仍有剩余差异的 test.etc 与未合并的 added.etc）
	dirMergeRes, err := diffService.MergeFiles(status.ID, []string{"etc"})
	if err != nil {
		t.Fatalf("MergeFiles by directory failed: %v", err)
	}
	if dirMergeRes.SuccessCount != 2 {
		t.Fatalf("Directory merge expected 2 remaining files merged, got: %+v", dirMergeRes)
	}
}

func TestArchiveDiffPartialSavePreservesRemainingDiff(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "base.pvf")
	pathB := filepath.Join(dir, "other.pvf")

	a := pvf.New()
	if _, err := a.AddFileText("etc/test.etc", "[name]\n`val_A`\n[price]\n`100`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAs(pathA); err != nil {
		t.Fatal(err)
	}

	b := pvf.New()
	if _, err := b.AddFileText("etc/test.etc", "[name]\n`val_B`\n[price]\n`200`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAs(pathB); err != nil {
		t.Fatal(err)
	}

	core := NewCore()
	openA, err := pvf.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.setArchive(openA); err != nil {
		t.Fatal(err)
	}
	defer core.closeArchive()

	diffService := NewArchiveDiffService(core)
	status, err := diffService.Start(ArchiveDiffRequest{Path: pathB})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		s, _ := diffService.Status(status.ID)
		if s.State == "ready" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 局部保存：只合入 [name]，但 [price] 仍然不同
	partialContent := "[name]\n`val_B`\n[price]\n`100`\n"
	if err := diffService.MergeText(status.ID, "etc/test.etc", partialContent); err != nil {
		t.Fatal(err)
	}

	// 验证局部保存后，etc/test.etc 仍然保留在差异列表中（因为 price 依然有差异）
	flat, err := diffService.FlatFiles(status.ID, "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || flat[0].Path != "etc/test.etc" {
		t.Fatalf("expected etc/test.etc to remain in diff list, got %+v", flat)
	}

	// 完全保存：内容完全与 B 一致
	fullContent := "[name]\n`val_B`\n[price]\n`200`\n"
	if err := diffService.MergeText(status.ID, "etc/test.etc", fullContent); err != nil {
		t.Fatal(err)
	}

	// 验证完全一致后，etc/test.etc 从差异列表中移除
	flatAfter, err := diffService.FlatFiles(status.ID, "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(flatAfter) != 0 {
		t.Fatalf("expected etc/test.etc to be removed from diff list, got %+v", flatAfter)
	}
}

func TestArchiveDiffMergeOneFilePreservesOtherDrafts(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "base.pvf")
	pathB := filepath.Join(dir, "other.pvf")

	a := pvf.New()
	if _, err := a.AddFileText("etc/file1.etc", "[name]\n`file1_orig`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFileText("etc/file2.etc", "[name]\n`file2_orig`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAs(pathA); err != nil {
		t.Fatal(err)
	}

	b := pvf.New()
	if _, err := b.AddFileText("etc/file1.etc", "[name]\n`file1_other`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddFileText("etc/file2.etc", "[name]\n`file2_other`\n", pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAs(pathB); err != nil {
		t.Fatal(err)
	}

	core := NewCore()
	openA, err := pvf.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.setArchive(openA); err != nil {
		t.Fatal(err)
	}
	defer core.closeArchive()

	diffService := NewArchiveDiffService(core)
	f1Idx, _ := core.archive.Find("etc/file1.etc")
	f2Idx, _ := core.archive.Find("etc/file2.etc")
	// 传入 file1 和 file2 的编辑器草稿
	status, err := diffService.Start(ArchiveDiffRequest{
		Path: pathB,
		Drafts: []ArchiveDiffDraft{
			{FileIndex: f1Idx, Path: "etc/file1.etc", Text: "[name]\n`file1_draft`\n"},
			{FileIndex: f2Idx, Path: "etc/file2.etc", Text: "[name]\n`file2_draft`\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		s, _ := diffService.Status(status.ID)
		if s.State == "ready" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 合并 file1
	_, err = diffService.MergeFiles(status.ID, []string{"etc/file1.etc"})
	if err != nil {
		t.Fatal(err)
	}

	// 获取 file2 的详情，验证 file2 的对比底稿仍为 file2_draft，而不是回退成磁盘的 file2_orig
	detail2, err := diffService.FileDiff(status.ID, "etc/file2.etc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail2.AfterText, "file2_draft") {
		t.Fatalf("expected file2 AfterText to preserve draft file2_draft, got: %q", detail2.AfterText)
	}
}
