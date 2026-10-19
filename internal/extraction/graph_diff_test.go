package extraction

import (
	"path"
	"strings"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// diffArchive builds a small in-memory archive so a cross-version comparison can
// be tested without two real PVF files.
func diffArchive(t *testing.T, members map[string]string) *pvf.Archive {
	t.Helper()
	created := pvf.New()
	for entryPath, text := range members {
		if _, err := created.AddFileText(entryPath, text, pvf.TypeScript); err != nil {
			t.Fatalf("AddFileText(%s): %v", entryPath, err)
		}
	}
	return created
}

func diffScanner(t *testing.T, archive *pvf.Archive) *Scanner {
	t.Helper()
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	specs := map[string]annotations.RelationSpec{
		"map":        {ListPath: "map/map.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"被动对象":       {ListPath: "passiveobject/passiveobject.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"monster":    {ListPath: "monster/monster.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"tile":       {ListPath: "map/tile.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
		"passiveobj": {ListPath: "passiveobject/passiveobject.lst", IDToken: 0, PathToken: 1, RecordTokens: 2},
	}
	return NewScanner(archive, catalog, NewRegistrySet(archive, specs))
}

func TestCompareEntriesClassifiesBothSides(t *testing.T) {
	left := diffArchive(t, map[string]string{
		"map/forest/a.map":            "[tile]\n\t`tiles/ground.til`\n",
		"map/forest/only.map":         "[tile]\n\t`tiles/ground.til`\n",
		"map/forest/tiles/ground.til": "[image]\n\t`tiles/ground.img`\n\t0\n",
	})
	right := diffArchive(t, map[string]string{
		// 同名但内容不同。
		"map/forest/a.map":            "[tile]\n\t`tiles/other.til`\n",
		"map/forest/new.map":          "[tile]\n\t`tiles/ground.til`\n",
		"map/forest/tiles/ground.til": "[image]\n\t`tiles/ground.img`\n\t0\n",
	})
	result, err := CompareEntries(left, right, 4)
	if err != nil {
		t.Fatalf("CompareEntries: %v", err)
	}
	// ground.til 在两边文本完全一样。它的载荷字节可能不同（字符串池槽位按归档分配），
	// 逐字节比较会把它误判成"内容变了"——这条断言就是这个回归。
	if result.Same != 1 {
		t.Errorf("应有 1 条两边内容相同, got %d（changed=%v）", result.Same, result.Changed)
	}
	if result.PoolRenumbered > result.Same {
		t.Errorf("池重排只能是内容相同的子集: pool=%d same=%d", result.PoolRenumbered, result.Same)
	}
	if !containsExactly(result.Changed, "map/forest/a.map") {
		t.Errorf("内容变化集合 = %v, want [map/forest/a.map]", result.Changed)
	}
	if len(result.Changed) != 1 || result.Changed[0] != "map/forest/a.map" {
		t.Errorf("内容变化集合 = %v, want [map/forest/a.map]", result.Changed)
	}
	if len(result.OnlyLeft) != 1 || result.OnlyLeft[0] != "map/forest/only.map" {
		t.Errorf("只在基线 = %v", result.OnlyLeft)
	}
	if len(result.OnlyRight) != 1 || result.OnlyRight[0] != "map/forest/new.map" {
		t.Errorf("只在对照 = %v", result.OnlyRight)
	}
	if result.Same+len(result.Changed)+len(result.OnlyLeft) != int(result.LeftEntries) {
		t.Errorf("条目层恒等式不成立: same=%d changed=%d onlyLeft=%d leftTotal=%d",
			result.Same, len(result.Changed), len(result.OnlyLeft), result.LeftEntries)
	}
	if int(result.RightEntries) != result.Same+len(result.Changed)+len(result.OnlyRight) {
		t.Errorf("对照侧恒等式不成立: same=%d changed=%d onlyRight=%d rightTotal=%d",
			result.Same, len(result.Changed), len(result.OnlyRight), result.RightEntries)
	}
}

// TestCompareReferencesReportsDriftNotCaseNoise is the semantic core: a
// reference that resolves on one side and misses on the other is drift, while a
// path written with different case or separators is the same reference.
func TestCompareReferencesReportsDriftNotCaseNoise(t *testing.T) {
	left := diffArchive(t, map[string]string{
		"map/map.lst":                 "1\t`forest/a.map`\n",
		"map/forest/a.map":            "[tile]\n\t`Tiles/Ground.TIL`\n",
		"map/forest/tiles/ground.til": "[image]\n\t`tiles/ground.img`\n\t0\n",
	})
	right := diffArchive(t, map[string]string{
		"map/map.lst": "1\t`forest/a.map`\n",
		// 同一条引用，只是大小写与分隔符不同。
		"map/forest/a.map": "[tile]\n\t`tiles\\ground.til`\n",
		// 目标不在了：这条才是漂移。
	})
	drifts, err := CompareReferences(diffScanner(t, left), diffScanner(t, right),
		[]string{"map/forest/a.map"}, nil)
	if err != nil {
		t.Fatalf("CompareReferences: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("大小写不同不该算漂移, got %+v", drifts)
	}
	drift := drifts[0]
	if len(drift.Dropped) != 1 || drift.Dropped[0].State != StateResolved.String() {
		t.Fatalf("应报告基线里那条已解析引用消失, got %+v", drift.Dropped)
	}
	if len(drift.Added) != 1 || drift.Added[0].State != StateMissing.String() {
		t.Fatalf("对照里同一条引用应变成 missing（状态本身就是漂移的一部分）, got %+v", drift.Added)
	}
	if drift.Same != 0 {
		t.Errorf("两边没有共同引用, got %d", drift.Same)
	}

	// 完全一致的两份归档不得报出任何漂移。
	sameArchive := diffArchive(t, map[string]string{
		"map/map.lst":                 "1\t`forest/a.map`\n",
		"map/forest/a.map":            "[tile]\n\t`tiles/ground.til`\n",
		"map/forest/tiles/ground.til": "[image]\n\t`tiles/ground.img`\n\t0\n",
	})
	quiet, err := CompareReferences(diffScanner(t, sameArchive), diffScanner(t, sameArchive),
		[]string{"map/forest/a.map"}, nil)
	if err != nil {
		t.Fatalf("CompareReferences: %v", err)
	}
	if len(quiet) != 0 {
		t.Fatalf("同一份归档自比不应有漂移, got %+v", quiet)
	}
}

func TestSummarizeDriftGroupsByEdge(t *testing.T) {
	drifts := []Drift{
		{Path: "a.dgn", Dropped: []RefLink{{EdgeID: "dgn.x", State: "resolved", Target: "t.tbl"}}},
		{Path: "b.dgn", Dropped: []RefLink{{EdgeID: "dgn.x", State: "resolved", Target: "t2.tbl"}}},
		{Path: "c.map", Added: []RefLink{{EdgeID: "map.y", State: "missing", Target: "z.ani"}}},
	}
	rows := SummarizeDrift(drifts)
	if len(rows) != 2 {
		t.Fatalf("应按边聚合为 2 类, got %+v", rows)
	}
	if rows[0].EdgeID != "dgn.x" || rows[0].Dropped != 2 || rows[0].Files != 2 {
		t.Errorf("漂移最多的边应排在首位, got %+v", rows[0])
	}
	if !strings.Contains(rows[0].Example, "a.dgn") {
		t.Errorf("汇总要能追到样例条目, got %q", rows[0].Example)
	}
}

func TestScopePathsMatchesDirectoryOrSubstring(t *testing.T) {
	paths := []string{"monster/a.mob", "map/x/monster/b.mob", "dungeon/c.dgn"}
	got := ScopePaths(paths, []string{"monster/"})
	if len(got) != 2 {
		t.Fatalf("前缀与子目录都应命中, got %v", got)
	}
	if len(ScopePaths(paths, nil)) != 3 {
		t.Error("空前缀应原样返回")
	}
	if len(ScopePaths(paths, []string{"nope"})) != 0 {
		t.Error("不该命中任何条目")
	}
	if path.Ext("a.mob") != ".mob" {
		t.Fatal("测试前提失效")
	}
}

// TestSameContentRejectsByteCoincidence is the mirror image of the pool
// renumbering case: two different strings can encode to identical payload bytes,
// because a payload stores string-pool slots and slot numbers are per-archive.
// Byte equality therefore proves nothing across archives, and this fixture is
// exactly that case (both payloads are 03 24 00 00 00 06 32 00 00 00).
func TestSameContentRejectsByteCoincidence(t *testing.T) {
	left := diffArchive(t, map[string]string{
		"map/forest/a.map": "[tile]\n\t`Tiles/Ground.TIL`\n",
	})
	right := diffArchive(t, map[string]string{
		"map/forest/a.map": "[tile]\n\t`tiles/other.til`\n",
	})
	leftIndex, _ := left.Find("map/forest/a.map")
	rightIndex, _ := right.Find("map/forest/a.map")
	leftBytes, _ := left.RawBytes(leftIndex)
	rightBytes, _ := right.RawBytes(rightIndex)
	if string(leftBytes) != string(rightBytes) {
		t.Skip("本例要求载荷字节相同，环境不同则跳过")
	}
	if got := sameContent(left, leftIndex, right, rightIndex); got != contentChanged {
		t.Fatalf("内容不同却被判为相同: %v（字节相同但字符串值不同）", got)
	}
}

func containsExactly(list []string, want string) bool {
	return len(list) == 1 && list[0] == want
}
