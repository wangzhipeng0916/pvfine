package extraction_test

import (
	"fmt"
	"strings"
	"testing"

	"pvfine/internal/extraction"
)

// presetSample is one seed a preset advertises, plus the closure size that seed
// was measured to produce. The floor is loose on purpose: content differs
// between archive revisions, so what this pins is "the advertised seed pulls real
// content", not 86's exact numbers.
type presetSample struct {
	preset     string
	seed       string
	minEntries int
}

var presetSamples = []presetSample{
	{"dungeon", "dungeon/act1/grakkarak.dgn", 100},
	{"dungeon", "地下城:7", 100},
	{"equipment", "装备:46700", 4},
	{"item", "stackable/gold.stk", 1},
	{"item", "道具:2660452", 1},
	{"giftbox", "stackable/cash/christmas_inboostersm.stk", 30},
	{"giftbox", "道具:86", 30},
	{"town", "town/silvercrown.twn", 30},
	{"town", "城镇:1", 30},
	{"combat", "怪物:1", 8},
	{"combat", "宠物:1", 8},
	{"aic", "APC:201", 10},
	{"worldmap", "副本接口:1", 1000},
}

// TestShippedPresetSeedsResolveOnRealArchive checks the promise every preset makes
// in its own text. Two failure modes matter more than closure size: a seed form no
// registry claims — 副本接口:38142 and 道具:4136001 were invented ID numbers, only
// caught by running them — and a hint edited away from a seed nobody re-measured.
func TestShippedPresetSeedsResolveOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	presets, err := extraction.LoadPresets(extraction.EmbeddedPresets, catalog)
	if err != nil {
		t.Fatalf("解析内置预设失败: %v", err)
	}

	for _, preset := range presets {
		samples := []presetSample{}
		for _, sample := range presetSamples {
			if sample.preset == preset.ID {
				samples = append(samples, sample)
			}
		}
		if len(samples) == 0 {
			t.Errorf("预设 %s 没有实测种子样本：新增预设要在这里登记一条真实种子", preset.ID)
			continue
		}
		alive, failures := 0, []string{}
		for _, sample := range samples {
			if !strings.Contains(preset.SeedHint, sample.seed) {
				t.Errorf("预设 %s 的说明里不再有 %q：种子写法一改就要重新实测", preset.ID, sample.seed)
				continue
			}
			hit, err := extraction.ResolveSeed(archive, scanner.RegistrySet(), sample.seed)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", sample.seed, err))
				continue
			}
			closure, err := scanner.Closure([]int32{hit.Index}, extraction.Options{})
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s 闭包失败: %v", sample.seed, err))
				continue
			}
			if len(closure.Nodes) < sample.minEntries {
				failures = append(failures,
					fmt.Sprintf("%s 闭包 %d 条目，低于 %d", sample.seed, len(closure.Nodes), sample.minEntries))
				continue
			}
			alive++
			t.Logf("%s %-52s → %d 条目 覆盖率 %.3f（via %s）",
				preset.ID, hit.Path, len(closure.Nodes), closure.Coverage(), hit.Via)
		}
		if alive == 0 {
			t.Errorf("预设 %s 的样本种子全部失效：%s", preset.ID, strings.Join(failures, "；"))
			continue
		}
		if len(failures) > 0 {
			t.Logf("预设 %s 有 %d 个样本在本归档失效（其余 %d 个仍可用）：%s",
				preset.ID, len(failures), alive, strings.Join(failures, "；"))
		}
	}
}
