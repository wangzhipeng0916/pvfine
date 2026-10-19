package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

// TestExtractionWorldmapScaleOnRealArchive runs the largest preset through the
// product path. Task #39 turned a `.wdm` seed from 1 entry into ~3,000, so this
// is the first closure big enough to question the preview's own cost: the
// content fingerprint, the asset inventory and the category table all scale with
// the closure. It asserts the shape is still delivered (including the new `ui`
// category, which only exists because worldmaps pull `.xui`) and logs the timing
// so a future regression shows up as a number, not a hang.
func TestExtractionWorldmapScaleOnRealArchive(t *testing.T) {
	archivePath := os.Getenv("PVF_TESTFILE")
	if archivePath == "" {
		t.Skip("PVF_TESTFILE 未设置，跳过真实归档测试")
	}
	archive, err := pvf.Open(archivePath)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	c := NewCore(newSettingsService(filepath.Join(t.TempDir(), "settings.json")))
	if err := c.setArchive(archive); err != nil {
		t.Fatalf("载入归档失败: %v", err)
	}
	t.Cleanup(c.closeArchive)
	svc := NewExtractionService(c)

	round := time.Now()
	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"副本接口:1"}, Preset: "worldmap"})
	elapsed := time.Since(round)
	if err != nil {
		t.Fatalf("Prepare(副本接口:1): %v", err)
	}
	if preview.Preset != "worldmap" {
		t.Errorf("预览要回显预设, got %q", preview.Preset)
	}
	if preview.ClosureCount < 1000 {
		t.Errorf("世界地图闭包应当是几千条量级，实得 %d —— 说明 wdm.* 边没生效", preview.ClosureCount)
	}
	t.Logf("世界地图闭包 %d 条目（主集 %d / 补入 %d），Prepare %s，词汇覆盖率 %.3f，引用图片 %d 个",
		preview.ClosureCount, preview.MainCount, preview.ExternalCount,
		elapsed.Round(time.Millisecond), preview.VocabCoverage, preview.AssetTotal)
	if elapsed > 60*time.Second {
		t.Errorf("Prepare 用了 %s，几千条目量级不该走到这个病态区间", elapsed)
	}

	files := map[string]int{}
	for _, row := range preview.Categories {
		files[row.ID] = row.Files
	}
	if files["ui"] == 0 {
		t.Errorf("世界地图必然带出 .xui 页面文件，ui 类别却为空：%v", files)
	}
	if files["scene"] == 0 || files["ani"] == 0 {
		t.Errorf("世界地图应带出副本与其动画：%v", files)
	}
	t.Logf("类别分布 %v", files)
}
