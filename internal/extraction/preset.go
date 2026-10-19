package extraction

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// EmbeddedPresets is the shipped preset list. Like the edge catalog it is
// embedded so the package owns its own data and cannot be run half-updated.
//
//go:embed edges/extract-presets.json
var EmbeddedPresets []byte

// Preset is one named starting point: what to select, which categories this kind
// of content usually needs, and a sentence about what comes along. It changes no
// traversal semantics — the edge catalog and the category selection do that.
type Preset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SeedHint    string   `json:"seedHint"`
	Categories  []string `json:"categories,omitempty"`
}

type presetDocument struct {
	Version int      `json:"version"`
	Presets []Preset `json:"presets"`
}

// PresetVersion is the schema this package understands.
const PresetVersion = 1

// LoadPresets parses and validates the preset document against a catalog, since
// a preset naming a category the catalog does not declare would silently select
// nothing at export time.
func LoadPresets(data []byte, catalog *Catalog) ([]Preset, error) {
	var doc presetDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("extraction: 解析提取预设失败: %w", err)
	}
	if doc.Version != PresetVersion {
		return nil, fmt.Errorf("extraction: 提取预设版本 %d 不被支持（当前 %d）", doc.Version, PresetVersion)
	}
	seen := map[string]bool{}
	for i, preset := range doc.Presets {
		id := strings.TrimSpace(preset.ID)
		if id == "" || seen[id] {
			return nil, fmt.Errorf("extraction: 提取预设第 %d 条的 id 为空或重复", i+1)
		}
		seen[id] = true
		if strings.TrimSpace(preset.Name) == "" {
			return nil, fmt.Errorf("extraction: 提取预设 %s 缺少名称", id)
		}
		for _, category := range preset.Categories {
			if catalog != nil && !catalog.HasCategory(category) {
				return nil, fmt.Errorf("extraction: 提取预设 %s 引用了未声明的类别 %q", id, category)
			}
		}
	}
	return doc.Presets, nil
}

// FindPreset returns the preset with this id, or nil when the id is empty or
// unknown. Callers must surface an unknown id rather than falling back silently.
func FindPreset(presets []Preset, id string) *Preset {
	if id == "" {
		return nil
	}
	for i := range presets {
		if presets[i].ID == id {
			return &presets[i]
		}
	}
	return nil
}
