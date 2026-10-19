package extraction

import (
	"strings"
	"testing"

	"pvfine/internal/annotations"
)

// TestResolveSeedAcceptsPathRelationAndID pins E4's seed forms. Paths used to be
// the only working one; the registry forms are what a MOD author actually has
// (a row number in a table), and they must resolve through the same tables the
// closure is walked with, not through a second lookup of their own.
func TestResolveSeedAcceptsPathRelationAndID(t *testing.T) {
	f := newFixture(t)
	registers := f.registrySet()

	hit, err := ResolveSeed(f.archive, registers, "map/forest/a.map")
	if err != nil {
		t.Fatalf("路径种子: %v", err)
	}
	if hit.Via != "path" || hit.Path != "map/forest/a.map" {
		t.Errorf("路径种子解析成 %+v", hit)
	}

	rel, err := ResolveSeed(f.archive, registers, "被动对象:10")
	if err != nil {
		t.Fatalf("关系名:ID 种子: %v", err)
	}
	if rel.Via != "relation" || rel.Relation != "被动对象" || rel.Path != "passiveobject/obj/door.obj" {
		t.Errorf("关系名:ID 解析成 %+v", rel)
	}

	// 纯 ID：10 只有"被动对象"表认，所以可以安全接受。
	bare, err := ResolveSeed(f.archive, registers, "10")
	if err != nil {
		t.Fatalf("纯 ID 种子: %v", err)
	}
	if bare.Via != "id" || bare.Path != "passiveobject/obj/door.obj" {
		t.Errorf("纯 ID 解析成 %+v", bare)
	}
}

// TestResolveSeedRefusesAmbiguousBareID is the reason the bare form is guarded:
// two tables using the same number is ordinary here, and choosing one would add
// a dependency nobody asked for.
func TestResolveSeedRefusesAmbiguousBareID(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"monster/monster.lst", "1\t`goblin.mob`\n"},
		{"monster/goblin.mob", "[name]\n\tgoblin\n"},
	}, testDocument())
	f.specs["怪物"] = annotations.RelationSpec{
		ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	registers := f.registrySet()

	if _, err := ResolveSeed(f.archive, registers, "1"); err == nil {
		t.Fatal("两个关系都认领 ID 1 时必须报错，不能挑一个")
	} else if !strings.Contains(err.Error(), "怪物") || !strings.Contains(err.Error(), "map") {
		t.Errorf("错误要列出候选关系, got %v", err)
	} else if !strings.Contains(err.Error(), "map:1") {
		t.Errorf("错误要给出可直接改写的写法, got %v", err)
	}

	// 显式关系名就确定了。
	explicit, err := ResolveSeed(f.archive, registers, "怪物:1")
	if err != nil {
		t.Fatalf("显式关系: %v", err)
	}
	if explicit.Path != "monster/goblin.mob" {
		t.Errorf("显式关系解析到 %s", explicit.Path)
	}
}

func TestResolveSeedExplainsWhatItCouldNotResolve(t *testing.T) {
	f := newFixture(t)
	registers := f.registrySet()

	if _, err := ResolveSeed(f.archive, registers, "map/forest/nope.map"); err == nil ||
		!strings.Contains(err.Error(), "关系名:ID") {
		t.Errorf("未命中要说明可写的形态, got %v", err)
	}
	if _, err := ResolveSeed(f.archive, registers, "map:999"); err == nil ||
		!strings.Contains(err.Error(), "无此 ID") {
		t.Errorf("表里查不到的 ID 要给出 registry 的原因, got %v", err)
	}
	// 带冒号但左边不是已知关系：那是个（不存在的）路径，不该被当成表查询。
	if _, err := ResolveSeed(f.archive, registers, "etc:whatever"); err == nil ||
		strings.Contains(err.Error(), "无此 ID") {
		t.Errorf("未知前缀不得当 registry 查询处理, got %v", err)
	}
	if _, err := ResolveSeed(f.archive, registers, "  "); err == nil {
		t.Error("空种子必须报错")
	}
	// 没有登记表时纯 ID 不该乱猜。
	if _, err := ResolveSeed(f.archive, nil, "10"); err == nil {
		t.Error("没有 registry 时纯 ID 应判不成")
	}
}
