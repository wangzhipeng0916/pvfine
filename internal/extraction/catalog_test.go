package extraction

import (
	"strings"
	"testing"
)

func TestLoadCatalogRejectsMalformedEdges(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"empty", `{"version":1,"edges":[]}`},
		{"missingID", `{"version":1,"edges":[{"extensions":[".map"],"section":"tile","target":{"kind":"entry"}}]}`},
		{"missingSection", `{"version":1,"edges":[{"id":"a","extensions":[".map"],"target":{"kind":"entry"}}]}`},
		{"missingExtensions", `{"version":1,"edges":[{"id":"a","section":"tile","target":{"kind":"entry"}}]}`},
		{"registryWithoutRelation", `{"version":1,"edges":[{"id":"a","extensions":[".map"],"section":"tile","target":{"kind":"registry"}}]}`},
		{"unknownKind", `{"version":1,"edges":[{"id":"a","extensions":[".map"],"section":"tile","target":{"kind":"nonsense"}}]}`},
		{"duplicateID", `{"version":1,"edges":[
			{"id":"a","extensions":[".map"],"section":"tile","target":{"kind":"entry"}},
			{"id":"a","extensions":[".map"],"section":"monster","target":{"kind":"entry"}}]}`},
		{"badExtension", `{"version":1,"edges":[{"id":"a","extensions":["map/file"],"section":"tile","target":{"kind":"entry"}}]}`},
		{"rootInteriorTraversal", `{"version":1,"edges":[{"id":"a","extensions":[".twn"],"section":"area","target":{"kind":"entry","roots":["etc/../.."]}}]}`},
		{"rootEmpty", `{"version":1,"edges":[{"id":"a","extensions":[".twn"],"section":"area","target":{"kind":"entry","roots":[" "]}}]}`},
		{"rootOnAsset", `{"version":1,"edges":[{"id":"a","extensions":[".twn"],"section":"area","target":{"kind":"asset","roots":["map"]}}]}`},
	}
	for _, tc := range cases {
		if _, err := LoadCatalog([]byte(tc.json)); err == nil {
			t.Errorf("%s: 期望报错但通过了", tc.name)
		}
	}
	// 合法 roots 必须能载入：静态类别根与相对 owner 的 ../ 两种形态都要收。
	for _, json := range []string{
		`{"version":1,"edges":[{"id":"a","extensions":[".twn"],"section":"area","target":{"kind":"entry","roots":["map"]}}]}`,
		`{"version":1,"edges":[{"id":"a","extensions":[".key"],"section":"particle filename","target":{"kind":"entry","roots":["../"]}}]}`,
		`{"version":1,"edges":[{"id":"a","extensions":[".key"],"section":"particle filename","target":{"kind":"entry","roots":["../../"]}}]}`,
	} {
		if _, err := LoadCatalog([]byte(json)); err != nil {
			t.Errorf("合法 roots 被拒: %v", err)
		}
	}
	if _, err := LoadCatalog([]byte(`{"version":1,"edges":[{"id":"a","extensions":[".key"],"section":"x","target":{"kind":"entry","roots":["key/../.."]}}]}`)); err == nil {
		t.Error("夹在中间的 .. 必须被拒")
	}
}

func TestCatalogIndexingIsCaseInsensitive(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{
		{ID: "act.baseAni", Extensions: []string{"act"}, Section: "BASE ANI",
			Target: Target{Kind: TargetEntry}},
		{ID: "map.special", Extensions: []string{".map"}, Section: "special passive object",
			Record: &Record{CountPrefixed: &CountPrefixed{PrefixTokens: 5, CountIndex: 4, ItemTokens: 6}},
			Target: Target{Kind: TargetRegistry, Relation: "物品"}},
	}}
	c, err := NewCatalog(doc)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	// 声明时省略前导点的扩展名与大小写混排的 section 都要能查到。
	if got := c.EdgesIn(".ACT", "base ani"); len(got) != 1 {
		t.Fatalf("EdgesIn 大小写/点号归一失败, got %d", len(got))
	}
	if got := c.EdgesIn(".map", "SPECIAL PASSIVE OBJECT"); len(got) != 1 {
		t.Fatalf("EdgesIn 未命中, got %d", len(got))
	}
	if cp := c.needsCountPrefixed(".map", "special passive object"); cp == nil || cp.ItemTokens != 6 {
		t.Error("计数前缀记录形状未被识别")
	}
	if cp := c.needsCountPrefixed(".ani", "special passive object"); cp != nil {
		t.Error(".ani 没有该边却返回了记录形状")
	}
	if !c.DeclaresSection(".map", "special passive object") {
		t.Error("DeclaresSection 漏判")
	}
	if rels := strings.Join(c.Relations(), ","); rels != "物品" {
		t.Errorf("Relations 应含 registry 目标, got %q", rels)
	}
}

func TestCountPrefixedValidation(t *testing.T) {
	base := func(cp *CountPrefixed) Document {
		return Document{Version: 1, Edges: []Edge{{
			ID: "map.special", Extensions: []string{".map"}, Section: "special passive object",
			Record: &Record{CountPrefixed: cp},
			Target: Target{Kind: TargetRegistry, Relation: "被动对象"},
		}}}
	}
	bad := []struct {
		name string
		cp   *CountPrefixed
	}{
		{"计数列越出前缀", &CountPrefixed{PrefixTokens: 5, CountIndex: 5, ItemTokens: 6}},
		{"计数列为负", &CountPrefixed{PrefixTokens: 5, CountIndex: -1, ItemTokens: 6}},
		{"前缀为零", &CountPrefixed{PrefixTokens: 0, CountIndex: 0, ItemTokens: 6}},
		{"条目宽度为零", &CountPrefixed{PrefixTokens: 5, CountIndex: 4, ItemTokens: 0}},
		{"kind 列越出条目", &CountPrefixed{PrefixTokens: 5, CountIndex: 4, ItemTokens: 6, KindIndex: 6}},
	}
	for _, tc := range bad {
		if _, err := NewCatalog(base(tc.cp)); err == nil {
			t.Errorf("%s: 期望报错但通过了", tc.name)
		}
	}
	if _, err := NewCatalog(base(&CountPrefixed{PrefixTokens: 5, CountIndex: 4, ItemTokens: 6, KindIndex: 0})); err != nil {
		t.Errorf("合法形状被拒: %v", err)
	}
}

func TestPositionAllowed(t *testing.T) {
	fixed := &Record{Width: 10, RefOffsets: []int{0}}
	for _, tc := range []struct {
		position int
		want     bool
	}{
		{0, true}, {10, true}, {9, false}, {1, false},
	} {
		if got := positionAllowed(fixed, tc.position); got != tc.want {
			t.Errorf("定宽 position=%d got=%v want=%v", tc.position, got, tc.want)
		}
	}
	multi := &Record{Width: 4, RefOffsets: []int{0, 3}}
	if !positionAllowed(multi, 3) || !positionAllowed(multi, 4) || positionAllowed(multi, 2) {
		t.Error("多引用列判定错误")
	}
	single := &Record{Width: 0}
	if !positionAllowed(single, 7) {
		t.Error("width<=1 应视为每列都是引用")
	}
}

func TestAncestorsAllowed(t *testing.T) {
	path := []string{"TRIGGER", "CHECKUP", "CREATE PASSIVEOBJECT", "INDEX"}
	if !ancestorsAllowed([]string{"create passiveobject"}, path) {
		t.Error("应允许祖先匹配")
	}
	if ancestorsAllowed([]string{"behavior"}, path) {
		t.Error("不存在的祖先被误允许")
	}
	if !ancestorsAllowed([]string{"trigger", "create passiveobject"}, path) {
		t.Error("多祖先顺序匹配失败")
	}
	if ancestorsAllowed([]string{"create passiveobject", "trigger"}, path) {
		t.Error("乱序祖先被误允许")
	}
}

func TestValueIsNoneUsesDefaults(t *testing.T) {
	cases := []struct {
		value string
		none  []string
		want  bool
	}{
		{"0", nil, true},
		{"-1", nil, true},
		{" 0 ", nil, true},
		{"100300001", nil, false},
		{"0", []string{}, true},    // 空列表回落到默认
		{"5", []string{"5"}, true}, // 自定义列表生效
		{"0", []string{"5"}, false},
		{"-1", []string{"5"}, false}, // 自定义列表替换而非叠加默认
	}
	for _, tc := range cases {
		if got := valueIsNone(tc.value, tc.none); got != tc.want {
			t.Errorf("valueIsNone(%q, %v) = %v, want %v", tc.value, tc.none, got, tc.want)
		}
	}
}
