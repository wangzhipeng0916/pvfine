package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// TestExtractionAssetsResolveToNPKOnRealData is the acceptance check for the
// resource half of an equipment package: the image paths the scripts reference
// must actually be findable inside the player's ImagePacks2, otherwise the sheet
// is decoration. It needs two local paths that are not in the repo, so it skips
// unless both are set: PVF_TESTFILE (a Script.pvf) and PVF_NPKDIR (an ImagePacks2
// directory). The image index is built into a temp cache, never the user's.
func TestExtractionAssetsResolveToNPKOnRealData(t *testing.T) {
	archivePath, npkDirectory := os.Getenv("PVF_TESTFILE"), os.Getenv("PVF_NPKDIR")
	if archivePath == "" || npkDirectory == "" {
		t.Skip("需要 PVF_TESTFILE 与 PVF_NPKDIR")
	}
	if _, err := os.Stat(npkDirectory); err != nil {
		t.Skipf("PVF_NPKDIR 不可访问: %v", err)
	}

	settings := newSettingsService(filepath.Join(t.TempDir(), "settings.json"))
	configured := DefaultAppSettings()
	configured.NPKDirectory = npkDirectory
	if err := settings.SaveSettings(configured); err != nil {
		t.Fatalf("写入测试设置失败: %v", err)
	}
	indexer := &ImageService{
		settings:  settings,
		cachePath: filepath.Join(t.TempDir(), "npk-image-index.json"),
		cache:     make(map[string]imageCacheEntry),
	}
	indexer.Initialize()
	status := waitImageIndexReady(t, indexer, 10*time.Minute)
	t.Logf("NPK 索引：%d 个容器 / %d 个 IMG / %d 帧，耗时 %.0f ms，重名 %d",
		status.NPKFiles, status.IMGFiles, status.ImageCount, status.BuildDurationMs, status.Duplicates)

	archive, err := pvf.Open(archivePath)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	c := NewCore(settings)
	if err := c.setArchive(archive); err != nil {
		t.Fatalf("载入归档失败: %v", err)
	}
	t.Cleanup(c.closeArchive)
	// 注入运行中的索引实例：它反映本次会话建好的索引，而不是磁盘上的旧副本。
	svc := NewExtractionService(c, indexer)

	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"dungeon/act1/grakkarak.dgn"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("副本闭包 %d 条目，引用图片 %d 个，其中归属到 NPK %d 个，动态模板 %d 个",
		preview.ClosureCount, preview.AssetTotal, preview.AssetMatched, preview.AssetDynamic)
	if preview.AssetTotal == 0 {
		t.Fatal("闭包里一个图片都没有，说明资产边或采集环节坏了")
	}
	if preview.AssetMatched == 0 {
		t.Error("图片在 NPK 索引里一个都找不到：要么配对的不是同一版本，要么归一化/别名逻辑坏了")
	}

	root := t.TempDir()
	result, err := svc.Execute(preview.ID, root)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "report", filepath.FromSlash(extraction.AssetFile)))
	if err != nil {
		t.Fatalf("资源对照表没落盘: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, ".NPK") {
		t.Errorf("对照表里应出现 NPK 归属:\n%s", firstLines(body, 8))
	}
	if result.AssetTotal != preview.AssetTotal || result.AssetMatched != preview.AssetMatched {
		t.Errorf("预览与落盘的资源计数必须一致: 预览 %d/%d, 落盘 %d/%d",
			preview.AssetMatched, preview.AssetTotal, result.AssetMatched, result.AssetTotal)
	}

	// 容器个数与体积决定"要不要顺手把 NPK 也复制进包"是便利还是灾难，先量出来。
	var total int64
	for _, container := range result.AssetContainers {
		info, err := os.Stat(filepath.Join(npkDirectory, filepath.FromSlash(container)))
		if err != nil {
			t.Errorf("索引给出的容器文件读不到: %s: %v", container, err)
			continue
		}
		total += info.Size()
	}
	t.Logf("本次引用落在 %d 个容器上，合计 %.1f MiB", len(result.AssetContainers), float64(total)/float64(1<<20))
	if len(result.AssetContainers) == 0 {
		t.Error("有归属图片却没解析出任何容器，说明 NPK 相对路径写法有问题")
	}
	if preview.AssetBytes != total {
		t.Errorf("预览算出的体积要等于实测: 预览 %d, 实际 %d", preview.AssetBytes, total)
	}

	// 勾选"把图片容器一起拷走"之后，产物必须自带这些容器；这一步验证的是端到端，
	// 不是统计——预览说多少就该写多少。
	withArtwork, err := svc.Prepare(ExtractionRequest{Seeds: []string{"dungeon/act1/grakkarak.dgn"}, CopyAssets: true})
	if err != nil {
		t.Fatalf("Prepare(带图片): %v", err)
	}
	if withArtwork.AssetBytes == 0 {
		t.Fatal("预览没算出可拷贝体积")
	}
	packaged, err := svc.Execute(withArtwork.ID, t.TempDir())
	if err != nil {
		t.Fatalf("Execute(带图片): %v", err)
	}
	if packaged.AssetCopied != len(packaged.AssetContainers) || packaged.AssetCopied == 0 {
		t.Errorf("应拷满预览列出的容器: %d / %d", packaged.AssetCopied, len(packaged.AssetContainers))
	}
	if packaged.AssetCopiedBytes != withArtwork.AssetBytes {
		t.Errorf("实拷字节数要与预览一致: %d vs %d", packaged.AssetCopiedBytes, withArtwork.AssetBytes)
	}
}

func waitImageIndexReady(t *testing.T, service *ImageService, budget time.Duration) ImageIndexStatus {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		status := service.IndexStatus()
		switch status.State {
		case ImageIndexStateReady:
			return status
		case ImageIndexStateError:
			t.Fatalf("NPK 索引失败: %s", status.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("NPK 索引超时: %#v", service.IndexStatus())
	return ImageIndexStatus{}
}

func firstLines(body string, count int) string {
	parts := strings.Split(body, "\n")
	if len(parts) > count {
		parts = parts[:count]
	}
	return strings.Join(parts, "\n")
}
