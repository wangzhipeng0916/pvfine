package services

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

// filterDiffChangesByScopes 根据选中的作用域列表（文件精确路径或目录前缀）过滤差异条目。
// @author flobit
func filterDiffChangesByScopes(changes []pvfversion.FileChange, scopes []string) []pvfversion.FileChange {
	if len(scopes) == 0 {
		return changes
	}
	exactPaths := make(map[string]struct{}, len(scopes))
	dirPrefixes := make([]string, 0, len(scopes))
	for _, raw := range scopes {
		scope := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
		scope = strings.Trim(scope, "/")
		if scope == "" {
			continue
		}
		exactPaths[scope] = struct{}{}
		dirPrefixes = append(dirPrefixes, scope+"/")
	}
	if len(exactPaths) == 0 && len(dirPrefixes) == 0 {
		return changes
	}

	filtered := make([]pvfversion.FileChange, 0, min(len(changes), len(scopes)))
	for _, change := range changes {
		path := change.Path
		if _, ok := exactPaths[path]; ok {
			filtered = append(filtered, change)
			continue
		}
		for _, prefix := range dirPrefixes {
			if strings.HasPrefix(path, prefix) {
				filtered = append(filtered, change)
				break
			}
		}
	}
	return filtered
}

// decodeDiffExportChange 读取并构建单个差异项的导出内容。
// @author flobit
func decodeDiffExportChange(change pvfversion.FileChange, mode string, reverse bool, source, baseline *exportSource, before *pvf.Archive) (exportChange, error) {
	operation := change.Operation
	if reverse {
		switch operation {
		case pvfversion.OperationAdd:
			operation = pvfversion.OperationDelete
		case pvfversion.OperationDelete:
			operation = pvfversion.OperationAdd
		}
	}
	item := exportChange{}
	var err error
	if operation == pvfversion.OperationDelete {
		if mode == "mod" {
			i, ok := before.Find(change.Path)
			if !ok {
				return exportChange{}, fmt.Errorf("差异基线文件不存在: %s", change.Path)
			}
			typ, _, _ := before.FileContentInfo(i)
			item.before = &exportContent{path: before.Path(i), dataType: typ}
		} else {
			item.before, err = baseline.read(change.Path)
		}
	} else {
		item.after, err = source.read(change.Path)
		if err == nil && mode == "mod" && operation == pvfversion.OperationModify && isExportMergeTable(change.Path) {
			item.before, err = baseline.read(change.Path)
		}
	}
	if err != nil {
		return exportChange{}, err
	}
	return item, nil
}

// decodeDiffExportChanges 并发或批量读取差异条目的导出内容。
// @author flobit
func decodeDiffExportChanges(changes []pvfversion.FileChange, mode string, reverse bool, source, baseline *exportSource, before *pvf.Archive) ([]exportChange, error) {
	total := len(changes)
	result := make([]exportChange, total)
	if total == 0 {
		return result, nil
	}

	workers := min(total, max(1, runtime.GOMAXPROCS(0)-1))
	if total <= 16 || workers <= 1 {
		for i, change := range changes {
			item, err := decodeDiffExportChange(change, mode, reverse, source, baseline, before)
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	}

	var firstErr atomic.Pointer[error]
	var wg sync.WaitGroup
	chunkSize := (total + workers - 1) / workers

	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := min(start+chunkSize, total)
		if start >= end {
			break
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				if firstErr.Load() != nil {
					return
				}
				item, err := decodeDiffExportChange(changes[i], mode, reverse, source, baseline, before)
				if err != nil {
					firstErr.CompareAndSwap(nil, &err)
					return
				}
				result[i] = item
			}
		}(start, end)
	}

	wg.Wait()
	if errPtr := firstErr.Load(); errPtr != nil {
		return nil, *errPtr
	}
	return result, nil
}

// The caller holds the core read lock. Take immutable snapshot references
// under the diff lock, then release it before decoding export content.
func (s *ExportService) archiveDiffExportSource(r ExportRequest) (*exportSource, []exportChange, error) {
	diff := s.c.archiveDiff
	if diff == nil {
		return nil, nil, fmt.Errorf("没有可导出的归档差异")
	}
	diff.mu.Lock()
	p, err := diff.findLocked(r.DiffID, true)
	if err != nil {
		diff.mu.Unlock()
		return nil, nil, err
	}
	before, after := p.other, p.current
	changes := make([]pvfversion.FileChange, 0, len(p.changes))
	for _, change := range p.changes {
		changes = append(changes, change)
	}
	diff.mu.Unlock()

	// 过滤用户选中的作用域（支持单文件与目录展开）
	changes = filterDiffChangesByScopes(changes, r.Scopes)
	if len(changes) == 0 {
		if len(r.Scopes) > 0 {
			return nil, nil, fmt.Errorf("选中的范围内没有差异文件")
		}
		return nil, nil, fmt.Errorf("没有可导出的差异文件")
	}

	if r.Reverse {
		before, after = after, before
	}
	source := &exportSource{archive: after, canonical: true}
	baseline := &exportSource{archive: before, canonical: true}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })

	result, err := decodeDiffExportChanges(changes, r.Mode, r.Reverse, source, baseline, before)
	if err != nil {
		return nil, nil, err
	}
	return source, result, nil
}
