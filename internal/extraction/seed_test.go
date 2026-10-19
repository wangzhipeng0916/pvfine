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

// TestResolveSeedAcceptsListRow covers the pasted .lst row: the form MOD authors
// actually have in hand is "id<TAB>path" out of the table, and the path column
// is what must be followed, because the id alone is only meaningful inside its
// own table.
func TestResolveSeedAcceptsListRow(t *testing.T) {
	f := newFixture(t)
	registers := f.registrySet()

	hit, err := ResolveSeed(f.archive, registers, "10\t`passiveobject/obj/door.obj`")
	if err != nil {
		t.Fatalf("lst 片段行: %v", err)
	}
	if hit.Via != "list" || hit.Path != "passiveobject/obj/door.obj" {
		t.Errorf("lst 片段行解析成 %+v", hit)
	}

	// 列序与列数不固定：从左往右取第一个能命中的列，`.lst` 之外的工具输出常带
	// 额外列（名称、注释），把它们当错行拒掉等于让用户手工切列。
	for _, line := range []string{
		"passiveobject/obj/door.obj\t10",
		"10\t门\tdoor\tpassiveobject/obj/door.obj",
		"10\tpassiveobject\\obj\\door.obj",
	} {
		hit, err := ResolveSeed(f.archive, registers, line)
		if err != nil {
			t.Errorf("%q: %v", line, err)
			continue
		}
		if hit.Via != "list" || hit.Path != "passiveobject/obj/door.obj" {
			t.Errorf("%q 解析成 %+v", line, hit)
		}
	}

	// 路径列在这份归档里不存在时必须报错，不能退回去按同一行的数字猜一个文件：
	// 行来自别的版本时，那个数字在这份归档里指向的可能是另一个文件。
	if _, err := ResolveSeed(f.archive, registers, "10\tpassiveobject/obj/gone.obj"); err == nil {
		t.Error("清单行的路径不存在时必须报错")
	} else if !strings.Contains(err.Error(), "passiveobject/obj/gone.obj") {
		t.Errorf("错误要带上整行内容, got %v", err)
	}
}

// TestResolveSeedPrefersWholeLinePath keeps a path that contains a space
// resolvable: the row scanner splits on whitespace, so the whole line has to be
// tried first or such a path would be shredded into columns and rejected.
func TestResolveSeedPrefersWholeLinePath(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"map/forest/my map.map", "[tile]\n\t`tiles/ground.til`\n"},
	}, testDocument())

	hit, err := ResolveSeed(f.archive, f.registrySet(), "map/forest/my map.map")
	if err != nil {
		t.Fatalf("含空格的路径: %v", err)
	}
	if hit.Via != "path" || hit.Path != "map/forest/my map.map" {
		t.Errorf("含空格的路径解析成 %+v", hit)
	}
}

// TestResolveSeedWithDeclaredTable covers 「清单所属表」: with the table named
// there is nothing to guess, so ids that are ambiguous across tables resolve
// through it — and a miss is reported as that table's miss instead of quietly
// trying the others.
func TestResolveSeedWithDeclaredTable(t *testing.T) {
	f := newFixtureFrom(t, []entry{
		{"monster/monster.lst", "1\t`goblin.mob`\n"},
		{"monster/goblin.mob", "[name]\n\tgoblin\n"},
	}, testDocument())
	f.specs["怪物"] = annotations.RelationSpec{
		ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2,
	}
	registers := f.registrySet()

	// ID 1 同时属于 map 与 怪物 两张表，声明了表就不必再猜。
	hit, err := ResolveSeedIn(f.archive, registers, "1", "怪物")
	if err != nil {
		t.Fatalf("声明表后的纯 ID: %v", err)
	}
	if hit.Via != "relation" || hit.Relation != "怪物" || hit.Path != "monster/goblin.mob" {
		t.Errorf("声明表后的纯 ID 解析成 %+v", hit)
	}

	// 表名写错要给出可选表名，而不是退回歧义报错。
	if _, err := ResolveSeedIn(f.archive, registers, "1", "怪"); err == nil {
		t.Error("未知表名必须报错")
	} else if !strings.Contains(err.Error(), "未知的清单所属表") || !strings.Contains(err.Error(), "怪物") {
		t.Errorf("未知表名的错误要列出可选表, got %v", err)
	}

	// 表里没有的 ID 是这张表的缺口，不该被别的表接走。
	if _, err := ResolveSeedIn(f.archive, registers, "999", "怪物"); err == nil {
		t.Error("声明表里没有的 ID 必须报错")
	} else if !strings.Contains(err.Error(), "怪物") || !strings.Contains(err.Error(), "999") {
		t.Errorf("错误要写清是哪张表缺这个 ID, got %v", err)
	}

	// 声明表不影响其他形态：路径种子照旧按路径解析。
	path, err := ResolveSeedIn(f.archive, registers, "map/forest/a.map", "怪物")
	if err != nil {
		t.Fatalf("声明表时的路径种子: %v", err)
	}
	if path.Via != "path" {
		t.Errorf("声明表时路径种子解析成 %+v", path)
	}
}
