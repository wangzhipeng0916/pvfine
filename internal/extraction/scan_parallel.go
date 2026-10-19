package extraction

import (
	"context"
	"sync"
	"sync/atomic"
)

// ScanStats summarizes a bulk scan.
type ScanStats struct {
	// Scanned counts entries whose text parsed successfully.
	Scanned int
	// Failed counts entries the archive could not hand back as a script.
	Failed int
}

// ScanIndices scans the given entries, optionally in parallel, and hands every
// successful result to visit.
//
// Parallelism is safe because the scan is read-only, but it only stays safe if
// the lazy machinery has already been paid for: call Prewarm first. A lazy
// basename index or registry load triggered inside a worker would race.
//
// visit is serialized, so callers can append to plain maps. The order is
// completion order, which is why sample values are chosen by content rather
// than by arrival.
func (s *Scanner) ScanIndices(ctx context.Context, indexes []int32, workers int, visit func(ScanResult)) (ScanStats, error) {
	var stats ScanStats
	if len(indexes) == 0 {
		return stats, nil
	}
	if s.basenames == nil {
		if err := s.prewarmBasenames(ctx); err != nil {
			return stats, err
		}
	}
	s.registers.PreloadAll(ctx)

	if workers <= 1 || len(indexes) == 1 {
		for _, index := range indexes {
			if ctx != nil && ctx.Err() != nil {
				return stats, ctx.Err()
			}
			result, err := s.ScanFile(index)
			if err != nil {
				stats.Failed++
				continue
			}
			stats.Scanned++
			if visit != nil {
				visit(result)
			}
		}
		return stats, nil
	}
	if workers > len(indexes) {
		workers = len(indexes)
	}

	var (
		next     int64
		mu       sync.Mutex
		failed   int64
		visited  int64
		firstErr error
	)
	work := func() {
		for {
			if ctx != nil && ctx.Err() != nil {
				return
			}
			at := atomic.AddInt64(&next, 1) - 1
			if at >= int64(len(indexes)) {
				return
			}
			result, err := s.ScanFile(indexes[at])
			if err != nil {
				atomic.AddInt64(&failed, 1)
				continue
			}
			atomic.AddInt64(&visited, 1)
			if visit != nil {
				mu.Lock()
				visit(result)
				mu.Unlock()
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			work()
		}()
	}
	wg.Wait()

	stats.Scanned = int(atomic.LoadInt64(&visited))
	stats.Failed = int(atomic.LoadInt64(&failed))
	if ctx != nil && ctx.Err() != nil {
		firstErr = ctx.Err()
	}
	return stats, firstErr
}
