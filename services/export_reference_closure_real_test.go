package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

// TestExportModReferenceClosureOnRealArchive measures the one thing the synthetic
// fixtures cannot answer: what reference-closure completion costs and adds on a
// real archive, and whether the numbers it reports are consistent with what the
// extraction engine computed for the same content.
//
// The comparison target is deliberate: the extraction CLI reported a 223-entry
// closure for this dungeon seed, so the MOD package should add roughly that many
// entries minus its own seed. If these two ever disagree wildly, one of the two
// paths stopped using the same edge catalog.
func TestExportModReferenceClosureOnRealArchive(t *testing.T) {
	file := os.Getenv("PVF_TESTFILE")
	if file == "" {
		t.Skip("PVF_TESTFILE 未设置，跳过真实归档测试")
	}
	if _, err := os.Stat(file); err != nil {
		t.Skipf("PVF_TESTFILE 不可访问: %v", err)
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	c := NewCore()
	if err := c.setArchive(archive); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	exports := NewExportService(c)

	writable := ""
	for _, descriptor := range exports.Formats() {
		if descriptor.CanWrite {
			writable = descriptor.ID
			break
		}
	}
	if writable == "" {
		t.Skip("没有可写的 mod 格式")
	}

	request := ExportRequest{
		Source: "selection", Mode: "mod", Format: writable, Name: "实测",
		Scopes: []string{"dungeon/act1/grakkarak.dgn"}, IncludeDependencies: true,
	}
	started := time.Now()
	preview, err := exports.Prepare(request)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	elapsed := time.Since(started)
	t.Logf("真实归档 MOD 补齐：包内条目=%d 文字表依赖=%d 引用闭包补入=%d 耗时=%s 警告=%d",
		preview.FileCount, preview.DependencyCount, preview.ReferenceDependencies,
		elapsed.Round(time.Millisecond), len(preview.Warnings))

	if preview.ReferenceDependencies < 100 {
		t.Errorf("副本种子应补出上百条脚本/动画依赖, got %d", preview.ReferenceDependencies)
	}
	if elapsed > 30*time.Second {
		t.Errorf("单副本范围的引用补齐超出预算: %s", elapsed)
	}
	anis := 0
	for _, entryPath := range packagePaths(preview) {
		if strings.HasSuffix(entryPath, ".ani") {
			anis++
		}
	}
	if anis == 0 {
		t.Error("补齐结果里没有一条 .ani，说明引用图没有真的走通")
	}
	t.Logf("补齐样本：.ani %d 条，首个警告 %q", anis, firstWarning(preview))
}

func packagePaths(preview *ExportPreview) []string {
	out := make([]string, 0, len(preview.Files))
	for _, file := range preview.Files {
		out = append(out, strings.ToLower(filepath.ToSlash(file.Path)))
	}
	return out
}

func firstWarning(preview *ExportPreview) string {
	if len(preview.Warnings) == 0 {
		return ""
	}
	return preview.Warnings[0]
}
