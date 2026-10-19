package extraction_test

import (
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

const (
	realDungeonSeed = "dungeon/act1/grakkarak.dgn"
	// 附录 A 实测：86 版该副本闭包 131 条目 / 29 ms。这里给足余量只做病态回归保护。
	realClosureBudget = 30 * time.Second
)

func openRealArchive(t *testing.T) *pvf.Archive {
	t.Helper()
	file := os.Getenv("PVF_TESTFILE")
	if file == "" {
		t.Skip("PVF_TESTFILE 未设置，跳过真实归档测试")
	}
	if _, err := os.Stat(file); err != nil {
		t.Skipf("PVF_TESTFILE 不可访问: %v", err)
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	return archive
}

// realRelationSpecs mirrors the relation set the product path would hand the
// scanner: config/lists.json plus the entries measured to exist in 86 but not
// yet registered there.
func realRelationSpecs(t *testing.T) map[string]annotations.RelationSpec {
	t.Helper()
	lists, err := annotations.ParseLists(config.ListsJSON)
	if err != nil {
		t.Fatalf("解析 lists.json 失败: %v", err)
	}
	specs := make(map[string]annotations.RelationSpec, len(lists.Relations)+1)
	for name, spec := range lists.Relations {
		specs[name] = spec
	}
	// config/lists.json 尚未登记 aicharacter/aicharacter.lst（实测 86 存在，1022 行）。
	// 真实产品路径应由 services 合并用户覆盖后传入，此处按同一形状补齐。
	if _, ok := specs["APC"]; !ok {
		specs["APC"] = annotations.RelationSpec{
			ListPath: "aicharacter/aicharacter.lst", IDToken: 0, PathToken: 1,
			RecordTokens: 2, NameSection: "name",
		}
	}
	return specs
}

func newRealScannerWith(t *testing.T, archive *pvf.Archive, catalog *extraction.Catalog) *extraction.Scanner {
	t.Helper()
	return extraction.NewScanner(archive, catalog, extraction.NewRegistrySet(archive, realRelationSpecs(t)))
}

func newRealScanner(t *testing.T, archive *pvf.Archive) *extraction.Scanner {
	t.Helper()
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	return newRealScannerWith(t, archive, catalog)
}

// TestMisdeclaredRecordShapeIsVisibleOnRealArchive is the successor of the
// kind-tag mutation oracle, now aimed at the count-prefixed shape. Pointing the
// count column at the kind tag must make the walk report `record-malformed`
// candidates instead of quietly returning the same references. Silence here is
// the failure mode that cost pvfsimple whole record classes.
func TestMisdeclaredRecordShapeIsVisibleOnRealArchive(t *testing.T) {
	archive := openRealArchive(t)
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	doc := catalog.Document()
	patched := false
	for i := range doc.Edges {
		if doc.Edges[i].ID != "map.specialPassiveObject" {
			continue
		}
		if doc.Edges[i].Record == nil || doc.Edges[i].Record.CountPrefixed == nil {
			t.Fatal("map.specialPassiveObject 未声明 countPrefixed 形状")
		}
		// 计数列指向 kind 标签：计数不再是整数，记录边界必然丢失。
		doc.Edges[i].Record.CountPrefixed.PrefixTokens = 6
		doc.Edges[i].Record.CountPrefixed.CountIndex = 5
		patched = true
	}
	if !patched {
		t.Fatal("边目录里找不到 map.specialPassiveObject")
	}
	mutated, err := extraction.NewCatalog(doc)
	if err != nil {
		t.Fatalf("改坏的目录未通过校验: %v", err)
	}

	// 取一个含本段的真实 map，避免依赖副本闭包恰好覆盖到它。
	const mapPath = "map/grakkarak/b1004.map"
	index, ok := archive.Find(mapPath)
	if !ok {
		t.Skipf("归档不含 %s", mapPath)
	}
	baseline, err := newRealScanner(t, archive).ScanFile(index)
	if err != nil {
		t.Fatalf("基线扫描失败: %v", err)
	}
	if len(baseline.Refs) == 0 {
		t.Skip("该 map 的 [special passive object] 没有产出引用")
	}
	after, err := newRealScannerWith(t, archive, mutated).ScanFile(index)
	if err != nil {
		t.Fatalf("变异扫描失败: %v", err)
	}
	var malformed int
	var sample string
	for _, u := range after.Unmodeled {
		if u.Shape == "record-malformed" {
			malformed++
			if sample == "" {
				sample = u.Value
			}
		}
	}
	if malformed == 0 {
		t.Errorf("声明错位后必须报 record-malformed，实际 0 条（基线引用 %d 条，变异后 %d 条）",
			len(baseline.Refs), len(after.Refs))
	}
	t.Logf("基线引用 %d 条 → 变异后引用 %d 条 / malformed %d 条（样例 %q）",
		len(baseline.Refs), len(after.Refs), malformed, sample)
}

func TestClosureRealDungeon(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	seed, ok := archive.Find(realDungeonSeed)
	if !ok {
		t.Fatalf("种子不存在: %s（归档版本可能不同）", realDungeonSeed)
	}
	started := time.Now()
	closure, err := scanner.Closure([]int32{seed}, extraction.Options{
		CollectUnmodeled: true,
		Cancel:           func() bool { return false },
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	if elapsed > realClosureBudget {
		t.Errorf("闭包耗时 %s 超出预算 %s", elapsed, realClosureBudget)
	}
	t.Logf("闭包=%d 条目 引用=%v 覆盖率=%.3f 耗时=%s 懒加载 registry=%d 回退命中=%d",
		len(closure.Nodes), closure.States, closure.Coverage(), elapsed.Round(time.Millisecond),
		scanner.RegistrySet().Loads(), scanner.RegistrySet().Fallbacks)

	if len(closure.Nodes) < 50 {
		t.Errorf("闭包条目过少: %d（附录 A 实测约 131）", len(closure.Nodes))
	}
	if closure.States[extraction.StateResolved] < 100 {
		t.Errorf("已解析引用过少: %d", closure.States[extraction.StateResolved])
	}

	exts := closure.Extensions()
	for _, required := range []string{".map", ".ani", ".mob"} {
		if exts[required] == 0 {
			t.Errorf("闭包缺少 %s，说明对应引用边失效（扩展名分布 %v）", required, exts)
		}
	}
	// 这两条是本项目相对 pvfsimple 的关键修正：86 版副本到地图的主通道，
	// 以及必须按源码行切记录的变宽被动对象段。
	if closure.ByEdge["dgn.mapSpecification"] == 0 {
		t.Error("[map specification] 边没有产出，副本到地图的引用已丢失")
	}
	if closure.ByEdge["map.specialPassiveObject"] == 0 {
		t.Error("[special passive object] 边没有产出，按行分组的变宽记录解析失效")
	}
	if closure.ByEdge["obj.stringData"] == 0 && exts[".obj"] > 0 {
		t.Error("附录 A.3 最大遗漏项 .obj [string data] 未生效")
	}
}

func TestClosureHonoursDepthAndCancel(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	seed, ok := archive.Find(realDungeonSeed)
	if !ok {
		t.Skip("种子不存在")
	}
	// 深度 1 只应含种子本身与其直接引用目标。
	shallow, err := scanner.Closure([]int32{seed}, extraction.Options{MaxDepth: 1})
	if err != nil {
		t.Fatalf("浅层闭包失败: %v", err)
	}
	deep, err := scanner.Closure([]int32{seed}, extraction.Options{})
	if err != nil {
		t.Fatalf("深层闭包失败: %v", err)
	}
	if len(shallow.Nodes) >= len(deep.Nodes) {
		t.Errorf("MaxDepth 未生效: 浅 %d >= 深 %d", len(shallow.Nodes), len(deep.Nodes))
	}
	for _, node := range shallow.Nodes {
		if node.Depth > 1 {
			t.Fatalf("MaxDepth=1 却出现 depth=%d (%s)", node.Depth, node.Path)
		}
	}
	if len(shallow.Nodes) < 2 {
		t.Errorf("深度 1 闭包应含直接引用, got %d", len(shallow.Nodes))
	}

	// 第二次轮询即返回取消信号，遍历应在扫描完首个条目后停止。
	permission := make(chan struct{})
	close(permission)
	<-permission
	polls := 0
	res, err := scanner.Closure([]int32{seed}, extraction.Options{Cancel: func() bool {
		polls++
		return polls > 1
	}})
	if err != extraction.ErrCancelled {
		t.Errorf("期望 ErrCancelled, got %v", err)
	}
	if res == nil || len(res.Nodes) > 1 {
		t.Errorf("取消后仍继续遍历: %v", res)
	}
	if polls < 2 {
		t.Errorf("Cancel 未被轮询, polls=%d", polls)
	}
}

func TestScanFileRealEntries(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	// 附录 A 实测过的代表性文件，逐个确认可解析且不解崩。
	for _, p := range []string{
		realDungeonSeed,
		"map/144_redcrystal_forest/55605_1776(1,0).map",
		"monster/goblin/goblin.mob",
		"aicharacter/_bizarre/atgunner/mirror_atgunner/action/proc.act",
		"aicharacter/swordman/kagemaru/kagemaru.aic",
		"town/silvercrown.twn",
		"stackable/gold.stk",
		"equipment/character/common/amulet/100300001.equ",
	} {
		index, ok := archive.Find(p)
		if !ok {
			t.Logf("跳过（该归档不含）: %s", p)
			continue
		}
		result, err := scanner.ScanFile(index)
		if err != nil {
			t.Errorf("ScanFile(%s) 失败: %v", p, err)
			continue
		}
		if result.Tokens == 0 {
			t.Errorf("%s 没有解析到任何值 token", p)
		}
		for _, ref := range result.Refs {
			if ref.State == extraction.StateResolved && ref.Kind != extraction.TargetAsset {
				if ref.Target < 0 || ref.TargetPath == "" {
					t.Errorf("%s 的已解析引用缺少目标: %+v", p, ref)
				}
				if archive.File(ref.Target).DataType == pvf.TypeScript &&
					strings.ToLower(path.Ext(ref.TargetPath)) == ".img" {
					t.Errorf("%s 把客户端资产解析成了归档条目: %+v", p, ref)
				}
			}
		}
	}
}
