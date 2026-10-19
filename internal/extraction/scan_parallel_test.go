package extraction

import (
	"context"
	"testing"
)

// TestScanIndicesMatchesSequential pins the parallel path against the
// sequential one. The race detector needs cgo, which this machine does not
// have, so equivalence is the practical guard: a worker that wrote shared state
// would show up as a differing aggregate.
func TestScanIndicesMatchesSequential(t *testing.T) {
	f := newFixture(t)
	scanner := f.scanner(t)
	indexes := make([]int32, 0, f.archive.FileCount())
	for index := int32(0); index < f.archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}

	collect := func(workers int) *Aggregator {
		t.Helper()
		aggregate := NewAggregator()
		stats, err := scanner.ScanIndices(context.Background(), indexes, workers, aggregate.AddScan)
		if err != nil {
			t.Fatalf("workers=%d 扫描失败: %v", workers, err)
		}
		if stats.Scanned == 0 || stats.Failed != 0 {
			t.Fatalf("workers=%d 统计异常: %+v", workers, stats)
		}
		return aggregate
	}

	sequential := collect(1)
	parallel := collect(4)
	if len(sequential.UnmodeledExample) != len(parallel.UnmodeledExample) {
		t.Fatalf("样本数不同: %d vs %d", len(sequential.UnmodeledExample), len(parallel.UnmodeledExample))
	}
	for key, want := range sequential.UnmodeledExample {
		if got := parallel.UnmodeledExample[key]; got != want {
			t.Errorf("样本不稳定 %s: %q vs %q", key, want, got)
		}
	}
	if len(sequential.Unmodeled) != len(parallel.Unmodeled) {
		t.Fatalf("缺口类别数不同: %d vs %d", len(sequential.Unmodeled), len(parallel.Unmodeled))
	}
	for key, want := range sequential.Unmodeled {
		if got := parallel.Unmodeled[key]; got != want {
			t.Errorf("缺口计数不同 %s: %d vs %d", key, want, got)
		}
	}
	for edge, want := range sequential.ByEdge {
		if got := parallel.ByEdge[edge]; got != want {
			t.Errorf("边命中不同 %s: %d vs %d", edge, want, got)
		}
	}
}

// TestScanIndicesHonoursCancellation: a cancelled context must stop the walk
// rather than finish it silently.
func TestScanIndicesHonoursCancellation(t *testing.T) {
	f := newFixture(t)
	scanner := f.scanner(t)
	indexes := make([]int32, 0, f.archive.FileCount())
	for index := int32(0); index < f.archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := scanner.ScanIndices(ctx, indexes, 2, nil)
	if err == nil {
		t.Errorf("已取消的上下文必须返回错误, stats=%+v", stats)
	}
	if stats.Scanned != 0 {
		t.Errorf("取消后不应继续扫描, got %d", stats.Scanned)
	}
}
