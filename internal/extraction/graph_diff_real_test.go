package extraction_test

import (
	"os"
	"runtime"
	"testing"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// TestGraphDiffHoldsOnTwoRealArchives checks the invariants that must hold for
// any pair of archives, without pinning numbers that belong to whichever two
// files happen to be on this machine. Set PVF_TESTFILE and PVF_TESTFILE2.
func TestGraphDiffHoldsOnTwoRealArchives(t *testing.T) {
	leftPath, rightPath := os.Getenv("PVF_TESTFILE"), os.Getenv("PVF_TESTFILE2")
	if leftPath == "" || rightPath == "" || leftPath == rightPath {
		t.Skip("需要 PVF_TESTFILE 与 PVF_TESTFILE2 两个不同归档")
	}
	left, err := pvf.Open(leftPath)
	if err != nil {
		t.Fatalf("打开基线失败: %v", err)
	}
	defer left.Release()
	right, err := pvf.Open(rightPath)
	if err != nil {
		t.Fatalf("打开对照失败: %v", err)
	}
	defer right.Release()

	entries, err := extraction.CompareEntries(left, right, runtime.NumCPU())
	if err != nil {
		t.Fatalf("CompareEntries: %v", err)
	}
	if entries.Same+len(entries.Changed)+len(entries.OnlyLeft) != int(entries.LeftEntries) {
		t.Errorf("基线侧恒等式不成立: same=%d changed=%d onlyLeft=%d total=%d",
			entries.Same, len(entries.Changed), len(entries.OnlyLeft), entries.LeftEntries)
	}
	if entries.Same+len(entries.Changed)+len(entries.OnlyRight) != int(entries.RightEntries) {
		t.Errorf("对照侧恒等式不成立: same=%d changed=%d onlyRight=%d total=%d",
			entries.Same, len(entries.Changed), len(entries.OnlyRight), entries.RightEntries)
	}
	for _, onlyLeft := range entries.OnlyLeft {
		if _, ok := right.Find(onlyLeft); ok {
			t.Fatalf("%s 被判成只在基线，对照里却找得到", onlyLeft)
		}
	}
	t.Logf("条目层：相同=%d（其中池重排 %d）内容变了=%d 只在基线=%d 只在对照=%d",
		entries.Same, entries.PoolRenumbered, len(entries.Changed),
		len(entries.OnlyLeft), len(entries.OnlyRight))

	// 引用层比对的是"内容变了的那批共同条目"；两份归档完全一致时没有可比对象。
	shared := entries.Changed
	if len(shared) > 3000 {
		shared = shared[:3000]
	}
	if len(shared) == 0 {
		t.Skip("两份归档内容完全一致，引用层无从比对")
	}
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	specs := realRelationSpecs(t)
	leftScanner := extraction.NewScanner(left, catalog, extraction.NewRegistrySet(left, specs))
	rightScanner := extraction.NewScanner(right, catalog, extraction.NewRegistrySet(right, specs))
	drifts, err := extraction.CompareReferences(leftScanner, rightScanner, shared, nil)
	if err != nil {
		t.Fatalf("CompareReferences: %v", err)
	}
	added, dropped := 0, 0
	for _, drift := range drifts {
		if !drift.Changed() {
			t.Errorf("%s 没有增减却被列为漂移", drift.Path)
		}
		added += len(drift.Added)
		dropped += len(drift.Dropped)
	}
	summaries := extraction.SummarizeDrift(drifts)
	totalAdded, totalDropped := 0, 0
	for _, row := range summaries {
		totalAdded += row.Added
		totalDropped += row.Dropped
	}
	if totalAdded != added || totalDropped != dropped {
		t.Errorf("汇总与明细不符: 明细 +%d/-%d 汇总 +%d/-%d", added, dropped, totalAdded, totalDropped)
	}
	t.Logf("引用层（抽样 %d 条）：受影响条目=%d 新增引用=%d 消失引用=%d 涉及边类=%d",
		len(shared), len(drifts), added, dropped, len(summaries))
	for at, row := range summaries {
		if at >= 5 {
			break
		}
		t.Logf("  %-28s +%d -%d 条目=%d 例=%s", row.EdgeID, row.Added, row.Dropped, row.Files, row.Example)
	}
}
