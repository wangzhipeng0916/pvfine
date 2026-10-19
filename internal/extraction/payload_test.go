package extraction

import (
	"os"
	"strings"
	"testing"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// testArchiveWithRelations opens the real archive and the relation set the
// product path would pass: the shipped lists plus the entries measured to exist
// in 86 but not yet registered there.
func testArchiveWithRelations(t *testing.T) (*pvf.Archive, *Scanner) {
	t.Helper()
	file := os.Getenv("PVF_TESTFILE")
	if file == "" {
		t.Skip("PVF_TESTFILE 未设置")
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	lists, err := annotations.ParseLists(config.ListsJSON)
	if err != nil {
		t.Fatalf("解析 lists.json 失败: %v", err)
	}
	specs := map[string]annotations.RelationSpec{}
	for name, spec := range lists.Relations {
		specs[name] = spec
	}
	if _, ok := specs["APC"]; !ok {
		specs["APC"] = annotations.RelationSpec{
			ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1,
			RecordTokens: 2, NameSection: "name",
		}
	}
	return archive, NewScanner(archive, catalog, NewRegistrySet(archive, specs))
}

// TestDeclaredPayloadSlotsHoldOnRealArchive is the standing guard for the
// per-kind payload declarations (#19). The check self-validates: the record's
// leading id is a known 被动对象 reference, so its hit rate is the ceiling a
// genuine reference can reach. Every declared slot must land in the same band;
// if a future revision changes the meaning of a column, the rate collapses and
// this test challenges the declaration instead of letting it rot silently.
func TestDeclaredPayloadSlotsHoldOnRealArchive(t *testing.T) {
	archive, scanner := testArchiveWithRelations(t)
	catalog := scanner.Catalog()
	var cp *CountPrefixed
	for _, edge := range catalog.Document().Edges {
		if edge.ID == "map.specialPassiveObject" {
			cp = edge.Record.CountPrefixed
		}
	}
	if cp == nil || len(cp.PayloadRefs) == 0 {
		t.Fatal("边目录未声明 map.specialPassiveObject 的载荷引用")
	}
	type key struct {
		kind string
		slot int
	}
	declared := map[key]string{}
	for _, ref := range cp.PayloadRefs {
		declared[key{ref.Kind, ref.Slot}] = ref.Relation
	}

	type tally struct{ hits, total int }
	measured := map[key]tally{}
	controlHits, controlTotal, maps := 0, 0, 0
	// 扫全库 .map（约 1.4 万，其中含该段的 5000+）。token 路径下全量只需数秒，
	// 而样本量直接决定这条守卫的证明力：真引用的命中率必须在大样本上成立。
	const mapCap = 1 << 30
	count := archive.FileCount()
	for i := int32(0); i < count && maps < mapCap; i++ {
		if !strings.HasSuffix(strings.ToLower(archive.FullPath(i)), ".map") {
			continue
		}
		view, err := archive.ScriptViewOf(i)
		if err != nil {
			continue
		}
		ords := []int{}
		for j := range view.Elements {
			el := &view.Elements[j]
			if el.Kind != pvf.ScriptElementToken || !strings.EqualFold(el.Section, "special passive object") {
				continue
			}
			ords = append(ords, j)
		}
		if len(ords) == 0 {
			continue
		}
		maps++
		tok := func(at int) *pvf.ScriptElement { return &view.Elements[ords[at]] }
		pos := 0
		for pos < len(ords) {
			if pos+cp.PrefixTokens > len(ords) {
				break
			}
			itemCount, ok := parseRecordCount(tok(pos + cp.CountIndex).Value)
			if !ok {
				break
			}
			if id := strings.TrimSpace(tok(pos).Value); id != "0" && id != "-1" {
				controlTotal++
				if target, err := scanner.RegistrySet().Resolve("被动对象", id); err == nil && target.Index >= 0 {
					controlHits++
				}
			}
			cursor := pos + cp.PrefixTokens
			for k := 0; k < itemCount; k++ {
				if cursor >= len(ords) {
					break
				}
				kind := normalizeTag(tok(cursor).Value)
				if containsFold(cp.NestedKinds, kind) {
					end := -1
					for at := cursor + 1; at < len(ords); at++ {
						if normalizeTag(tok(at).Value) == "/"+kind {
							end = at
							break
						}
					}
					if end < 0 {
						break
					}
					cursor = end + 1
					continue
				}
				for slot := cp.KindIndex + 1; slot < cp.ItemTokens; slot++ {
					relation, isDeclared := declared[key{kind, slot}]
					if !isDeclared || cursor+slot >= len(ords) {
						continue
					}
					el := tok(cursor + slot)
					if el.TokenType != 0 || el.Value == "0" || el.Value == "-1" {
						continue
					}
					row := measured[key{kind, slot}]
					row.total++
					if target, err := scanner.RegistrySet().Resolve(relation, el.Value); err == nil && target.Index >= 0 {
						row.hits++
					}
					measured[key{kind, slot}] = row
				}
				cursor += cp.ItemTokens
			}
			pos = cursor
		}
	}
	if maps == 0 {
		t.Skip("该归档没有含 [special passive object] 的 .map")
	}
	if controlTotal == 0 {
		t.Fatal("对照列为空，判定方法无法自证")
	}
	controlRate := 100 * float64(controlHits) / float64(controlTotal)
	t.Logf("对照：记录首列命中率 %d/%d = %.1f%%", controlHits, controlTotal, controlRate)
	if controlRate < 99 {
		t.Fatalf("对照列命中率仅 %.1f%%，判定方法本身失效，不能据此评估声明", controlRate)
	}
	for _, ref := range cp.PayloadRefs {
		row := measured[key{ref.Kind, ref.Slot}]
		if row.total == 0 {
			t.Errorf("%s 槽 %d 没采到样本，声明无法验证", ref.Kind, ref.Slot)
			continue
		}
		rate := 100 * float64(row.hits) / float64(row.total)
		t.Logf("%s 槽 %d → %s：%d/%d = %.1f%%", ref.Kind, ref.Slot, ref.Relation, row.hits, row.total, rate)
		if rate < 99 {
			t.Errorf("%s 槽 %d 命中 %s 只有 %.1f%%（对照 %.1f%% 说明方法可用）——语义可能已变，声明需复核",
				ref.Kind, ref.Slot, ref.Relation, rate, controlRate)
		}
	}
}
