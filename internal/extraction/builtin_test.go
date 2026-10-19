package extraction_test

import (
	"strings"
	"testing"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
)

// TestBuiltinCatalogIsValid guards the shipped edge data: it must parse, and
// every relation it names must exist in the shipped list definitions, apart from
// the documented exceptions. A catalog that silently references a missing
// relation would produce unresolved references instead of failing loudly.
func TestBuiltinCatalogIsValid(t *testing.T) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("内置边目录加载失败: %v", err)
	}
	if catalog.EdgeCount() == 0 {
		t.Fatal("内置边目录为空")
	}
	lists, err := annotations.ParseLists(config.ListsJSON)
	if err != nil {
		t.Fatalf("解析 lists.json 失败: %v", err)
	}
	// config/lists.json 尚未登记 aicharacter/aicharacter.lst（实测 86 存在该表）。
	// 该项已记录为 docs/提取中心设计.md 附录 A 的待补项。
	declaredButMissing := map[string]bool{"APC": true}
	for _, relation := range catalog.Relations() {
		if _, ok := lists.Relations[relation]; ok {
			continue
		}
		if declaredButMissing[relation] {
			continue
		}
		t.Errorf("边目录引用了 lists.json 中不存在的 relation: %q", relation)
	}
	// 分类表里的 relation 同样必须真实存在，否则该类别永远匹配不到东西，
	// 而且不会有任何报错——正是那种"配置写错了却静默生效"的情形。
	for _, category := range catalog.Categories() {
		for _, relation := range category.Relations {
			if _, ok := lists.Relations[relation]; ok {
				continue
			}
			if declaredButMissing[relation] {
				continue
			}
			t.Errorf("类别 %s 引用了 lists.json 中不存在的 relation: %q", category.ID, relation)
		}
	}
}

func TestBuiltinCatalogCoversMeasuredEdges(t *testing.T) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	// 这些是附录 A 实测确认的引用位，回归保护它们不被误删。
	wanted := []struct {
		ext, section string
	}{
		{".dgn", "map specification"}, // 86 版副本到地图的主通道
		{".map", "passive object"},
		{".map", "special passive object"},
		{".act", "base ani"},
		{".mob", "waiting motion"},
		{".obj", "string data"},
		{".obj", "basic motion"},
		{".twn", "area"},
		{".aic", "ai pattern"},
		{".als", "use animation"},
	}
	for _, tc := range wanted {
		if len(catalog.EdgesIn(tc.ext, tc.section)) == 0 {
			t.Errorf("内置目录缺少实测已确认的边: %s %s", tc.ext, tc.section)
		}
	}
	if !catalog.DeclaresSection(".ani", "image") {
		t.Error(".ani [image] 应声明为资产边以抑制 Unmodeled 噪声")
	}
	// 资产边必须存在，否则 icon 会被当成未解析条目。
	for _, ext := range []string{".equ", ".stk"} {
		edges := catalog.EdgesIn(ext, "icon")
		if len(edges) == 0 || edges[0].Target.Kind != extraction.TargetAsset {
			t.Errorf("%s 的 [icon] 应声明为 asset 边", ext)
		}
	}
}

func TestBuiltinCatalogExtensionCoverage(t *testing.T) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	seen := map[string]int{}
	for _, edge := range catalog.Document().Edges {
		if edge.Target.Kind == extraction.TargetRegistry && edge.Record != nil &&
			edge.Record.CountPrefixed == nil && edge.Record.Width > 1 && len(edge.Record.RefOffsets) == 0 {
			t.Errorf("registry 边 %s 定宽但未声明 refOffsets，将默认只取第 0 列", edge.ID)
		}
		for _, ext := range edge.Extensions {
			seen[strings.ToLower(ext)]++
		}
	}
	// 副本预设所需的扩展名必须齐备。
	for _, ext := range []string{".dgn", ".map", ".mob", ".obj", ".act", ".als", ".ani", ".til", ".aic", ".twn"} {
		if seen[ext] == 0 {
			t.Errorf("内置目录未覆盖 %s", ext)
		}
	}
}
