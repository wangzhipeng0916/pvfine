package extraction

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestShippedPresetsMatchTheCatalog keeps the preset list from drifting away
// from the catalog: a preset may only name categories the catalog declares, and
// the equipment family must stay described because its edges are the ones that
// were established by measurement in #34/#37.
func TestShippedPresetsMatchTheCatalog(t *testing.T) {
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	presets, err := LoadPresets(EmbeddedPresets, catalog)
	if err != nil {
		t.Fatalf("解析内置预设失败: %v", err)
	}
	if len(presets) < 8 {
		t.Fatalf("预设数量异常: %d", len(presets))
	}
	byID := map[string]Preset{}
	for _, preset := range presets {
		byID[preset.ID] = preset
	}
	for _, want := range []string{"dungeon", "equipment", "item", "giftbox", "town", "combat", "aic", "worldmap"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("缺少预设 %s", want)
		}
	}
	// 每个预设都要既说清「闭包有多大」也说清「怎么给种子」：这些数字是实测来的，
	// 少了数字，用户看到 1 个条目会以为提取坏了。
	for _, preset := range presets {
		if preset.Description == "" || preset.SeedHint == "" {
			t.Errorf("预设 %s 缺说明或种子写法", preset.ID)
		}
	}
	for _, id := range []string{"town", "combat", "aic", "worldmap"} {
		preset := byID[id]
		if !strings.Contains(preset.Description, "条目") {
			t.Errorf("预设 %s 的说明要带实测闭包规模, got %q", id, preset.Description)
		}
	}
	// worldmap 与 dungeon 一样用空类别＝全部类别，所以只核这四条窄预设。
	for _, id := range []string{"equipment", "item", "giftbox", "town", "combat", "aic"} {
		if len(byID[id].Categories) == 0 {
			t.Errorf("预设 %s 要给出默认类别", id)
		}
	}
	if got := byID["equipment"]; len(got.Categories) == 0 || got.SeedHint == "" {
		t.Errorf("装备预设要给出默认类别与种子写法: %+v", got)
	}
	if !strings.Contains(byID["equipment"].Description, "NPK") {
		t.Error("装备预设的说明必须提到图片容器，那是它的主产物之一")
	}

	broken := `{"version":1,"presets":[{"id":"x","name":"X","categories":["nope"]}]}`
	if _, err := LoadPresets([]byte(broken), catalog); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("引用未声明类别的预设必须加载失败并指名: %v", err)
	}
	duplicate := `{"version":1,"presets":[{"id":"x","name":"X"},{"id":"x","name":"X2"}]}`
	if _, err := LoadPresets([]byte(duplicate), catalog); err == nil {
		t.Error("重复 id 必须失败")
	}
	if _, err := LoadPresets([]byte(`{"version":99}`), catalog); err == nil {
		t.Error("版本不认识时要拒绝加载，而不是当成空列表")
	}
	var probe presetDocument
	if err := json.Unmarshal(EmbeddedPresets, &probe); err != nil {
		t.Fatalf("内置预设不是合法 JSON: %v", err)
	}
}
