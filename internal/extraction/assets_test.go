package extraction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssetInventoryReportsAttributionAndDynamicPaths pins the resource half of
// an export: every image the exported scripts reference must appear, a located
// one must name its container, a template must be reported as undecidable and
// not as missing, and an unlocated one must say so. The distinction is the whole
// point — an equipment package whose images are silently absent looks complete.
func TestAssetInventoryReportsAttributionAndDynamicPaths(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"equipment/equ/test.equ", "[icon]\n\t`Item/Icon/weapon.img`\n\t7\n[field image]\n\t`Item/FieldImage.img`\n\t2\n"},
		{"stackable/dyn.stk", "[icon]\n\t`Item/cash/%d.img`\n\t0\n"},
	}, testDocument())
	defer f.archive.Release()

	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载内置边目录失败: %v", err)
	}
	scanner := NewScanner(f.archive, catalog, f.registrySet())
	if err := scanner.Prewarm(nil); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	indexes := make([]int32, 0, 2)
	for _, p := range []string{"equipment/equ/test.equ", "stackable/dyn.stk"} {
		index, ok := f.archive.Find(p)
		if !ok {
			t.Fatalf("夹具缺少 %s", p)
		}
		indexes = append(indexes, index)
	}
	closure, err := scanner.Closure(indexes, Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}

	exported := make([]int32, 0, len(closure.Nodes))
	for _, node := range closure.Nodes {
		exported = append(exported, node.Index)
	}
	inventory, err := BuildAssetInventory(f.archive, scanner, exported, func(imagePath string) (string, bool) {
		if strings.EqualFold(imagePath, "Item/Icon/weapon.img") {
			return "sprite_item_icon.NPK", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("BuildAssetInventory: %v", err)
	}
	if inventory.Total != 3 {
		t.Errorf("应收集到 3 个图片路径, got %d: %+v", inventory.Total, inventory.Records)
	}
	if inventory.Matched != 1 {
		t.Errorf("应有 1 个归属到 NPK, got %d", inventory.Matched)
	}
	if inventory.Dynamic != 1 {
		t.Errorf("模板路径应记为动态而不是缺失, got %d", inventory.Dynamic)
	}
	if inventory.Unmatched != 1 {
		t.Errorf("未命中的应恰好 1 个, got %d", inventory.Unmatched)
	}

	root := t.TempDir()
	result, err := Export(f.archive, closure, ExportOptions{Dir: root, WithReports: true, Scanner: scanner,
		Assets: func(imagePath string) (string, bool) {
			if strings.EqualFold(imagePath, "Item/Icon/weapon.img") {
				return "sprite_item_icon.NPK", true
			}
			return "", false
		}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "report", AssetFile))
	if err != nil {
		t.Fatalf("资源对照表没落盘: %v", err)
	}
	for _, want := range []string{"sprite_item_icon.NPK\tItem/Icon/weapon.img", "动态", "索引里没有"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("资源对照表缺少 %q:\n%s", want, data)
		}
	}
	if result.Manifest.Counts["assets"] != 3 || result.Manifest.Counts["assetsMatched"] != 1 {
		t.Errorf("manifest 资源计数不对: %v", result.Manifest.Counts)
	}
	if len(result.Manifest.Assets) != 3 {
		t.Errorf("manifest 应带上完整资源清单, got %d", len(result.Manifest.Assets))
	}

	// 没有索引时也必须产出这张表，并把"没索引"说清楚——静默省略就等于回到旧行为。
	noLocator, err := BuildAssetInventory(f.archive, scanner, exported, nil)
	if err != nil {
		t.Fatalf("BuildAssetInventory(无定位器): %v", err)
	}
	if noLocator.Indexed || noLocator.Matched != 0 || noLocator.Total != 3 {
		t.Errorf("无索引时应只列路径且明写未启用: %+v", noLocator)
	}
	if body := AssetReport(noLocator, 2); !strings.Contains(body, "没有可用的 NPK 索引") {
		t.Errorf("无索引的说明要写在表头:\n%s", body)
	}
}
