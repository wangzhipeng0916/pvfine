package extraction_test

import (
	"strings"
	"testing"

	"pvfine/config"
	"pvfine/internal/extraction"
	"pvfine/internal/rendering"
)

// lintWidthSource adapts the shipped rendering rules to the width audit. It
// uses the version-independent width: the lint runs without an archive open,
// and a version override only ever narrows an already declared rule.
type lintWidthSource struct{ engine *rendering.Engine }

func (l lintWidthSource) DeclaredWidth(filePath, section string) (int, bool) {
	spec := l.engine.SectionFormat(filePath, section)
	if spec.TokensPerLine <= 1 {
		return 0, false
	}
	return spec.TokensPerLine, true
}

// widthDeclarationWhitelist lists the record widths that rendering has no rule
// for, keyed "edgeID|ext|section". Registration is deliberately manual: the
// point of the lint is that adding a width nobody can cross-check must be a
// conscious act with a recorded reason, not a line that slips in unnoticed.
// Every entry below was measured against real 86 records (see the edge's own
// note field for the sample that established it).
var widthDeclarationWhitelist = map[string]string{
	"map.specialPassiveObject|.map|special passive object":    "计数前缀记录（5467/5467 精确消费，18012 条），宽度不是常数，rendering 无法表达",
	"dgn.mapSpecification|.dgn|map specification":             "记录为 `\"map\" x y mapID` 四列，ID 在第 4 列；86 副本到地图的主通道",
	"map.monster|.map|monster":                                "出生记录按 10 列切；知识库 monster-entry-link-router 记为 8 列以上数值，故只取首列",
	"map.passiveObject|.map|passive object":                   "4 列定宽，首列为被动对象 ID",
	"map.animation|.map|animation":                            "5 列定宽，末列为动画路径",
	"map.aiCharacter|.map|ai character":                       "4 列定宽，首列为 APC registry ID",
	"act.summonMonster|.act|summon monster":                   "3 列定宽，首列为怪物 ID",
	"mob.dropItem|.mob|item":                                  "(`物品ID`,`概率`) 对，86 实测 634 行字段数恒为偶数",
	"mob.deathTowerItem|.mob|death tower item":                "同掉落对偶：86 实测 22 行，2 列×20 / 4 列×2",
	"mob.superChampionDropItem|.mob|super champion drop item": "86 实测 16 行全部 2 列",
	"mob.catchItem|.mob|catch item":                           "86 实测仅 4 行，均 3 列",
	"etc.equipmentPartSet|.etc|equipment part set":            "段形如 `id / 套装 .equ / 套装名` 三行头部 + 8 行 `名称 [部位] 2 0` 部件行；rendering 的 4 列规则管的是部件行，而引用（.equ 路径）在头部行，两者不是同一批 token。86 实测该段路径型值 2,415 条按 equipment/ 根 2,415/2,415=1.000 解析。",
	// 以下 17 条是任务 #34 的装备/道具/礼包 ID 型边：rendering 里没有对应 section 的
	// 宽度规则，宽度全部由 86 全归档实测与知识库 OfficialAnnotationTranslation 的
	// 带注释样本共同确定，逐条数字写在边目录的 note 里。
	"equ.summonMonster|.equ|summon monster":                "3 列 = (怪物ID, 等级, 最大数量)；实测 218 值 → 217（0.995），等级/数量列不是引用。",
	"equ.summonApc|.equ|summon apc":                        "3 列 = (APC ID, 等级, 数量)；实测 102 → 102（1.000）。与 .stk 侧的 5 列不同形，故分两条边。",
	"stk.summonApc|.stk|summon apc":                        "5 列 = (APC ID, 等级(-1=同自身), 未知, 持续ms, 数量)；实测 239 → 239（1.000）。",
	"stk.monsterSummons|.stk|monster summons":              "5 列 = (怪物ID, 最低等级, 最高等级, 持续ms, 最大召唤数)；实测 108 → 108（1.000）。",
	"equ.emancipateInput|.equ|input":                       "[emancipate] 内 1/2/4 对 = (道具ID, 数量)；实测 16300 → 16300（1.000）。同名 [input] 在 AI 按键段是别的语义，故边上加了 under。",
	"equ.emancipateOutput|.equ|output":                     "(装备ID, 数量)；实测 9918 → 9899（0.998）。同段按宽度 1 声明只得 0.499，宽度由此二选一钉死。",
	"equ.needMaterial|.equ|need material":                  "2 列 = (道具ID, 数量)，非闭合块；实测 8396 → 8383（0.998）。",
	"stk.needMaterial|.stk|need material":                  "同上形状；实测 5161 → 5001（0.969）。",
	"equ.consumeItem|.equ|consume item":                    "[then] 内 (道具ID, 数量)；实测 596 → 596（1.000）。",
	"stk.consumeItem|.stk|consume item":                    "闭合块多行 (道具ID, 数量)；实测 12 → 12（1.000），样本少故只登记不推广。",
	"stk.boosterEquipment|.stk|equipment":                  "首行是条目数、条目 = (装备ID, 相对概率, 数量) 三列；首行把扁平流错位一格，所以 ID 在第 1 列：col0 实测 0.000 而 col1 0.984。",
	"stk.boosterStackable|.stk|stackable":                  "同一错位机理，3 列取第 1 列；实测 16344 → 16273（0.996）。",
	"stk.boosterCera|.stk|cera":                            "3 列取第 1 列；实测 9016 → 8849（0.981）。col0 在稠密并表上同为 0.99 级、二者不可分，按知识库 stackablesample-booster.stk 的形状取 col1。",
	"stk.boosterEtc|.stk|etc":                              "3 列取第 1 列；实测 108943 → 108566（0.997）。与 config/annotations.json 既有规则 `.stk [etc] index 1 → 物品` 同口径。",
	"stk.boosterCreature|.stk|creature":                    "3 列取第 1 列；按 宠物 表首测 390 值 0 命中，改并表 物品 后 382/390（0.979）⇒ 礼盒里的 creature 子块装的是宠物蛋 .equ，不是 .cre。",
	"stk.boosterAvatar|.stk|avatar":                        "5 列 = (装备ID, 概率, 数量, 期限, 属性) 取第 1 列；col0 实测 0.002、col1 0.972。时装类容器装装备而非道具。",
	"stk.packageDataSelection|.stk|package data selection": "2 列 = (ID, 数量) 取第 0 列；实测 1615 → 1615（1.000）。知识库写『候选 ID + 数量列，精确宽度未封死』，故只声明已实测到的两列形。",
	// 任务 #39 的两条世界地图边：rendering 里没有这两个 section 的宽度规则。
	"wdm.dungeon|.wdm|dungeon":                     "2 列 = (副本ID, -1)；86 实测 col0 268→257（0.959），col1 只剩 12 个值→1（0.083），因为第二列大多是 `-1`、按 DefaultNoneValues 记 unknown 不进解析数。知识库同时警告块内可嵌 [in progress]，那些数值属另一个段。",
	"wdm.hellFreepassItem|.wdm|hell freepass item": "2 列 = (门票物品ID, 数量)；实测 593→593（1.000）。对照列 col1 同为 1.000，但那是数量 1 撞上了 物品 并表里的 ID 1（§16.2 记过的假高分），取 col0 的依据是知识库语义。",
}

// TestBuiltinWidthDeclarationsAreAccountedFor turns 判据 2 from a printed
// warning into an assertion: a catalog width either matches the rendering rule
// for the same section, or it is registered in the whitelist above.
func TestBuiltinWidthDeclarationsAreAccountedFor(t *testing.T) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	engine, err := rendering.Parse(config.RenderingJSON)
	if err != nil {
		t.Fatalf("解析 rendering.json 失败: %v", err)
	}
	findings := extraction.WidthAudit(catalog, lintWidthSource{engine},
		func(ext string) string { return "sample" + ext })

	var mismatches, unlisted, declaredOnly []string
	matched := map[string]bool{}
	for _, finding := range findings {
		key := finding.EdgeID + "|" + finding.Extension + "|" + strings.ToLower(finding.Section)
		switch finding.Kind {
		case "mismatch":
			mismatches = append(mismatches, finding.String())
		case "edge-only", "data-driven", "no-claim":
			matched[key] = true
			if _, ok := widthDeclarationWhitelist[key]; !ok {
				unlisted = append(unlisted, key+"  ← "+finding.String())
			}
		case "declared-only":
			declaredOnly = append(declaredOnly, finding.String())
		}
	}
	for _, line := range mismatches {
		t.Errorf("宽度与 rendering 冲突: %s", line)
	}
	for _, line := range unlisted {
		t.Errorf("未登记的 edge-only 宽度（若是实测确认的，请加入 widthDeclarationWhitelist）: %s", line)
	}
	// 白名单同样要防腐：条目一旦不再是 edge-only（例如 rendering 补了规则、
	// 或边被删除），登记项必须一起清掉，否则它会在无人注意时掩盖新问题。
	for key := range widthDeclarationWhitelist {
		if !matched[key] {
			t.Errorf("白名单条目已过期（不再是不受交叉验证的宽度声明）: %s", key)
		}
	}
	for _, line := range declaredOnly {
		t.Logf("rendering 有宽度规则但边目录未取引用（可能是漏边）: %s", line)
	}
	t.Logf("审计结论: 冲突=%d 未登记=%d 仅渲染侧=%d 白名单=%d",
		len(mismatches), len(unlisted), len(declaredOnly), len(matched))
}
