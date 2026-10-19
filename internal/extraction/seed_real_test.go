package extraction_test

import (
	"sort"
	"strings"
	"testing"

	"pvfine/internal/extraction"
)

// TestResolveSeedOnRealArchive checks the ID seed form against the shipped
// relation tables rather than a fixture: whatever a table actually claims must
// come back as the same entry the table points at, and a number no table claims
// must fail loudly instead of returning an empty plan.
func TestResolveSeedOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	registers := extraction.NewRegistrySet(archive, realRelationSpecs(t))

	checked := 0
	for _, relation := range registers.Relations() {
		indexes, err := registers.PathIndex(relation)
		if err != nil || len(indexes) == 0 {
			continue
		}
		// PathIndex 是 path→ID；取两条当样本，正反两个方向都要能对上。
		taken := 0
		for entryPath, id := range indexes {
			hit, err := extraction.ResolveSeed(archive, registers, relation+":"+id)
			if err != nil {
				t.Fatalf("%s:%s 解析失败: %v", relation, id, err)
			}
			if hit.Via != "relation" || hit.Path != entryPath {
				t.Errorf("%s:%s 解到 %s（via %s），表里写的是 %s",
					relation, id, hit.Path, hit.Via, entryPath)
			}
			checked++
			taken++
			if taken >= 2 {
				break
			}
		}
	}
	if checked < 10 {
		t.Fatalf("真实归档上只核对了 %d 条 ID 种子，样本太少说明测试没跑到登记表", checked)
	}
	t.Logf("真实归档核对 ID 种子 %d 条", checked)

	// 纯 ID 只在归属唯一时才接受；这张表里几乎每个 ID 都有多个关系认领，
	// 所以这里用"表里没有的号段"验证失败路径而不是猜中。
	if _, err := extraction.ResolveSeed(archive, registers, "999999999"); err == nil {
		t.Error("不存在的 ID 必须报错，不能给一个空种子集")
	}
}

// TestBareIDUniquenessOnRealArchive measures whether the bare-ID seed form is
// actually usable, before recommending it in the UI. Each sampled ID is looked
// up in every relation; the interesting number is how many land on exactly one.
func TestBareIDUniquenessOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	registers := extraction.NewRegistrySet(archive, realRelationSpecs(t))

	sample := map[string]bool{}
	relations := registers.Relations()
	for _, relation := range relations {
		indexes, err := registers.PathIndex(relation)
		if err != nil {
			continue
		}
		taken := 0
		for _, id := range indexes {
			sample[relation+"|"+id] = true
			taken++
			if taken >= 40 {
				break
			}
		}
	}

	unique, ambiguous, dead := 0, 0, 0
	exampleAmbiguous := ""
	for key := range sample {
		own, id := splitKeySeed(key)
		owners := []string{}
		for _, relation := range relations {
			target, err := registers.Resolve(relation, id)
			if err == nil && target.Index >= 0 {
				owners = append(owners, relation)
			}
		}
		switch len(owners) {
		case 0:
			dead++
		case 1:
			unique++
		default:
			ambiguous++
			if exampleAmbiguous == "" {
				exampleAmbiguous = id + " 属于 " + joinRelations(owners) + "（ sampled from " + own + "）"
			}
		}
	}
	total := unique + ambiguous + dead
	t.Logf("真实归档纯 ID 归属抽样 %d 条：唯一 %d（%.1f%%）歧义 %d（%.1f%%）查不到 %d",
		total, unique, 100*float64(unique)/float64(total), ambiguous,
		100*float64(ambiguous)/float64(total), dead)
	if exampleAmbiguous != "" {
		t.Logf("歧义样例：%s", exampleAmbiguous)
	}
}

func splitKeySeed(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

func joinRelations(names []string) string {
	out := ""
	for at, name := range names {
		if at > 0 {
			out += "、"
		}
		out += name
	}
	return out
}

// TestListRowSeedsResolveEveryEquipmentRow is the acceptance check for
// 「按清单提取」: every row of a real registry, written the way a .lst line reads,
// must resolve — and must land on exactly the entry its id resolves to. A rate
// below 1.000 would mean a pasted list silently drops rows, which is the failure
// mode this feature exists to avoid.
func TestListRowSeedsResolveEveryEquipmentRow(t *testing.T) {
	archive := openRealArchive(t)
	registers := extraction.NewRegistrySet(archive, realRelationSpecs(t))
	byPath, err := registers.PathIndex("装备")
	if err != nil {
		t.Fatalf("读装备登记表: %v", err)
	}
	if len(byPath) < 1000 {
		t.Fatalf("装备表只有 %d 行，测试没跑到登记表", len(byPath))
	}

	type row struct{ id, path string }
	rows := make([]row, 0, len(byPath))
	for entryPath, id := range byPath {
		rows = append(rows, row{id: id, path: entryPath})
	}
	sort.Slice(rows, func(i, j int) bool {
		if len(rows[i].id) != len(rows[j].id) {
			return len(rows[i].id) < len(rows[j].id)
		}
		return rows[i].id < rows[j].id
	})

	spaced := 0
	for _, one := range rows {
		if strings.Contains(one.path, " ") {
			spaced++
		}
	}
	if spaced == 0 {
		t.Error("样本里没有带空格的路径，跨列取路径的规则在这份归档上就没被覆盖到")
	}

	for _, one := range rows {
		hit, err := extraction.ResolveSeedIn(archive, registers, one.id+"\t"+one.path, "")
		if err != nil {
			t.Fatalf("清单行 %s\t%s 解析失败: %v", one.id, one.path, err)
		}
		if hit.Via != "list" {
			t.Fatalf("清单行 %s 走的是 %q，应当按路径列解析", one.id, hit.Via)
		}
		byID, err := extraction.ResolveSeedIn(archive, registers, one.id, "装备")
		if err != nil {
			t.Fatalf("装备:%s 解析失败: %v", one.id, err)
		}
		if hit.Index != byID.Index {
			t.Fatalf("%s 的片段行与 ID 解析到不同条目：%s / %s", one.id, hit.Path, byID.Path)
		}
	}
	t.Logf("装备表 %d 行按清单行全部解析（其中 %d 行路径含空格），且与 装备:ID 落到同一条目",
		len(rows), spaced)

	// 三类坏行各自要报错，而且消息要带上问题本身，否则 200 行里的一行错行没法定位。
	if _, err := extraction.ResolveSeedIn(archive, registers, "1\tequipment/character/nonexistent/x.equ", ""); err == nil {
		t.Error("路径不存在的清单行必须报错")
	} else if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("错误要带上行内容, got %v", err)
	}
	if _, err := extraction.ResolveSeedIn(archive, registers, "999999999", "装备"); err == nil {
		t.Error("声明表里没有的 ID 必须报错")
	}
	if _, err := extraction.ResolveSeedIn(archive, registers, "1\tnot/a/path\tAlso/Not", ""); err == nil {
		t.Error("所有列都不命中的清单行必须报错")
	}
}
