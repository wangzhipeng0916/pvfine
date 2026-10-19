package extraction_test

import (
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
