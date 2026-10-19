// Package services provides archive management and editing services.
// Author: flobit
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

// ArchiveDiffMergeResult 记录批量合并操作的结果汇总
type ArchiveDiffMergeResult struct {
	SuccessCount int      `json:"successCount"`
	FailedCount  int      `json:"failedCount"`
	MergedPaths  []string `json:"mergedPaths"`
	Errors       []string `json:"errors,omitempty"`
}

// MergeFiles 将选中的差异文件从对比归档合并到本地当前打开的归档中。
// 遵循核心锁在前、diff 锁在后的顺序防止死锁。
// @author flobit
func (s *ArchiveDiffService) MergeFiles(id string, paths []string) (*ArchiveDiffMergeResult, error) {
	if len(paths) == 0 {
		return &ArchiveDiffMergeResult{}, nil
	}

	// 锁顺序：先 core 后 diff
	s.c.mu.Lock()

	a := s.c.archive
	if a == nil {
		s.c.mu.Unlock()
		return nil, ErrNoArchive
	}
	if err := s.c.ensureVersionReadyLocked(); err != nil {
		s.c.mu.Unlock()
		return nil, err
	}

	s.mu.Lock()
	p, err := s.findLocked(id, true)
	if err != nil {
		s.mu.Unlock()
		s.c.mu.Unlock()
		return nil, err
	}
	otherArchive := p.other
	if otherArchive == nil {
		s.mu.Unlock()
		s.c.mu.Unlock()
		return nil, errors.New("对比归档已失效，请重新比对")
	}
	actualPaths := expandSelectedLeafPaths(p.changes, paths)
	s.mu.Unlock()

	if len(actualPaths) == 0 {
		s.c.mu.Unlock()
		return &ArchiveDiffMergeResult{}, nil
	}

	mutationCheckpoint := a.MutationCheckpoint()
	var before pvfversion.ContentSnapshot
	if s.c.versionRepo != nil {
		before, _ = pvfversion.ContentSnapshotFromArchive(a, actualPaths)
	}

	mergedPaths, structuralChanged, res := applyDiffMergesLocked(a, otherArchive, actualPaths)

	if structuralChanged {
		if err := s.c.rebuildArchiveIndexesLocked(a); err != nil {
			s.c.mu.Unlock()
			return nil, err
		}
	}

	stage := a.CloneForBatch()
	s.finalizeMergeMutationLocked(a, mutationCheckpoint, before, mergedPaths, -1, structuralChanged, "合并归档差异", "files-merged")
	s.updateSessionAfterMerge(id, mergedPaths, stage)

	return res, nil
}

// applyDiffMergesLocked 批量应用差异合并或删除操作，返回成功路径与是否触发结构性变更。
// @author flobit
func applyDiffMergesLocked(target, other *pvf.Archive, paths []string) ([]string, bool, *ArchiveDiffMergeResult) {
	res := &ArchiveDiffMergeResult{MergedPaths: make([]string, 0, len(paths))}
	structuralChanged := false

	for _, rawPath := range paths {
		canonicalKey := pvfversion.CanonicalPath(rawPath)
		tIdx, hasOther := other.Find(canonicalKey)

		var structural bool
		var mergeErr error
		if hasOther {
			structural, mergeErr = mergeDiffFileEntry(target, other, rawPath, tIdx)
		} else {
			structural, mergeErr = deleteDiffFileEntry(target, rawPath)
		}

		if mergeErr != nil {
			res.FailedCount++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", rawPath, mergeErr))
			continue
		}
		if structural {
			structuralChanged = true
		}
		res.SuccessCount++
		res.MergedPaths = append(res.MergedPaths, rawPath)
	}
	return res.MergedPaths, structuralChanged, res
}

// MergeText 将单文件编辑或段落合入后的最终文本写回到当前工作区归档。
// @author flobit
func (s *ArchiveDiffService) MergeText(id, path, text string) error {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return errors.New("文件路径不能为空")
	}

	s.c.mu.Lock()

	a := s.c.archive
	if a == nil {
		s.c.mu.Unlock()
		return ErrNoArchive
	}
	if err := s.c.ensureVersionReadyLocked(); err != nil {
		s.c.mu.Unlock()
		return err
	}

	s.mu.Lock()
	_, err := s.findLocked(id, true)
	s.mu.Unlock()
	if err != nil {
		s.c.mu.Unlock()
		return err
	}

	mutationCheckpoint := a.MutationCheckpoint()
	var before pvfversion.ContentSnapshot
	if s.c.versionRepo != nil {
		before, _ = pvfversion.ContentSnapshotFromArchive(a, []string{cleanPath})
	}

	canonicalKey := pvfversion.CanonicalPath(cleanPath)
	targetIdx, exists := a.Find(canonicalKey)
	structuralChanged := false

	if exists {
		if err := a.SetText(targetIdx, text); err != nil {
			s.c.mu.Unlock()
			return err
		}
	} else {
		dataType := int32(pvf.TypeScript)
		if strings.EqualFold(filepath.Ext(cleanPath), ".str") {
			dataType = pvf.TypeUnicode
		}
		newIdx, err := a.AddFileText(cleanPath, text, dataType)
		if err != nil {
			s.c.mu.Unlock()
			return err
		}
		targetIdx = newIdx
		structuralChanged = true
	}

	if structuralChanged {
		if err := s.c.rebuildArchiveIndexesLocked(a); err != nil {
			s.c.mu.Unlock()
			return err
		}
	}

	stage := a.CloneForBatch()
	s.finalizeMergeMutationLocked(a, mutationCheckpoint, before, []string{cleanPath}, targetIdx, structuralChanged, "合并脚本内容", "file-updated")
	s.updateSessionAfterMerge(id, []string{cleanPath}, stage)

	return nil
}

// finalizeMergeMutationLocked 统一完成合并成功后的版本记录、缓存失效及前端事件派发。
// 注意：该函数执行期间会释放 s.c.mu 互斥锁。
// @author flobit
func (s *ArchiveDiffService) finalizeMergeMutationLocked(
	a *pvf.Archive,
	checkpoint int,
	before pvfversion.ContentSnapshot,
	paths []string,
	targetIdx int32,
	structuralChanged bool,
	actionName string,
	versionAction string,
) {
	if s.c.versionRepo != nil && len(paths) > 0 {
		after, _ := pvfversion.ContentSnapshotFromArchive(a, paths)
		_ = s.c.recordVersionMutationLocked(actionName, before, after)
	}

	s.c.invalidateScriptLocked()
	s.c.editorAnnotation = editorAnnotationCache{}
	s.c.annotationRelations = make(map[string]map[string]*relationTarget)
	s.c.invalidateAdvancedSearchLocked()

	mutationSummary := a.MutationsSince(checkpoint)
	a.ClearMutations()
	info := a.Info()
	versioned := s.c.versionRepo != nil
	s.c.mu.Unlock()

	s.c.scheduleArchiveMutations(a, mutationSummary)
	emitEvent("archive:changed", info)
	emitEvent("archive:advanced-search-stale", map[string]any{"batch": true})
	payload := map[string]any{
		"source":        "archive-diff",
		"paths":         paths,
		"structural":    structuralChanged,
		"modifiedCount": info.ModifiedCount,
	}
	if targetIdx >= 0 {
		payload["fileIndexes"] = []int32{targetIdx}
	}
	emitEvent("archive:batch-applied", payload)
	if versioned {
		emitVersionState(s.c, versionAction)
	}
}

// diffPathBase 提取路径的纯文件名，兼容 Windows 与正斜杠路径
func diffPathBase(p string) string {
	return path.Base(strings.ReplaceAll(p, "\\", "/"))
}

// FlatFiles 按照搜索词与操作类型返回所有差异文件的平铺列表，供双栏工作台过滤检索
func (s *ArchiveDiffService) FlatFiles(id, query, operation string) ([]ArchiveDiffNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.findLocked(id, true)
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(strings.TrimSpace(query))
	opFilter := strings.ToLower(strings.TrimSpace(operation))

	result := make([]ArchiveDiffNode, 0)
	for _, change := range p.changes {
		op := string(change.Operation)
		if opFilter != "" && opFilter != "all" && op != opFilter {
			continue
		}
		display := change.DisplayPath
		if q != "" && !strings.Contains(strings.ToLower(display), q) {
			continue
		}
		result = append(result, ArchiveDiffNode{
			Path:      display,
			Name:      diffPathBase(display),
			IsDir:     false,
			Operation: op,
			Count:     1,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		pi, pj := strings.ToLower(result[i].Path), strings.ToLower(result[j].Path)
		if pi != pj {
			return pi < pj
		}
		return result[i].Path < result[j].Path
	})

	return result, nil
}

// updateSessionAfterMerge 在合并写入后更新对比会话的 internal changes 缓存与统计。
// 对于局部修改的文件，精确复核其与对比归档之间是否仍然存在差异，保留剩余差异条目。
// @author flobit
func (s *ArchiveDiffService) updateSessionAfterMerge(id string, mergedPaths []string, stage *pvf.Archive) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.findLocked(id, false)
	if err != nil || p.changes == nil {
		return
	}
	if p.drafts != nil {
		for _, mp := range mergedPaths {
			delete(p.drafts, pvfversion.CanonicalPath(mp))
		}
	}
	if stage != nil {
		p.current = stage
		if p.current != nil && len(p.drafts) > 0 {
			for canonicalKey, draft := range p.drafts {
				if idx, ok := p.current.Find(canonicalKey); ok {
					_ = p.current.SetText(idx, draft.Text)
				}
			}
		}
	}

	// A previously merged file may have disappeared from changes and then be edited again.
	for _, mp := range mergedPaths {
		key := pvfversion.CanonicalPath(mp)
		if change, hasDiff := archiveDiffCheckSingleFileChange(p.other, p.current, key, mp); hasDiff {
			p.changes[key] = change
		} else {
			delete(p.changes, key)
		}
	}

	remainingChanges := make([]pvfversion.FileChange, 0, len(p.changes))
	var added, deleted, modified int

	for _, change := range p.changes {
		remainingChanges = append(remainingChanges, change)
		switch change.Operation {
		case pvfversion.OperationAdd:
			added++
		case pvfversion.OperationDelete:
			deleted++
		default:
			modified++
		}
	}

	changeIndex := make(map[string]pvfversion.FileChange, len(remainingChanges))
	for _, change := range remainingChanges {
		changeIndex[change.Path] = change
	}
	p.changes = changeIndex
	p.status.Added, p.status.Deleted, p.status.Modified = added, deleted, modified
	p.detail = nil

	children, filteredChildren, err := buildAllDiffTrees(context.Background(), remainingChanges, nil)
	if err == nil {
		p.children = children
		p.filteredChildren = filteredChildren
	}
}

// archiveDiffCheckSingleFileChange 检查单个文件在合并或修改后，与对比归档之间是否仍然存在差异。
// 若无差异则返回 false，通知会话从差异树中移除该文件；若仍有差异则返回更新后的变更信息。
func archiveDiffCheckSingleFileChange(before, after *pvf.Archive, canonicalKey, displayFallback string) (pvfversion.FileChange, bool) {
	if before == nil || after == nil {
		return pvfversion.FileChange{}, false
	}
	bi, bok := before.Find(canonicalKey)
	ai, aok := after.Find(canonicalKey)
	if !bok && !aok {
		return pvfversion.FileChange{}, false
	}
	if !bok && aok {
		aType, _, err := after.FileContentInfo(ai)
		if err != nil {
			return pvfversion.FileChange{}, false
		}
		disp := after.Path(ai)
		if disp == "" {
			disp = displayFallback
		}
		return pvfversion.FileChange{
			Path:        canonicalKey,
			DisplayPath: pvfversion.DisplayPath(disp),
			Operation:   pvfversion.OperationAdd,
			AfterType:   aType,
		}, true
	}
	if bok && !aok {
		bType, _, err := before.FileContentInfo(bi)
		if err != nil {
			return pvfversion.FileChange{}, false
		}
		disp := before.Path(bi)
		if disp == "" {
			disp = displayFallback
		}
		return pvfversion.FileChange{
			Path:        canonicalKey,
			DisplayPath: pvfversion.DisplayPath(disp),
			Operation:   pvfversion.OperationDelete,
			BeforeType:  bType,
		}, true
	}

	// 双方均存在条目
	bType, bSize, err1 := before.FileContentInfo(bi)
	aType, aSize, err2 := after.FileContentInfo(ai)
	if err1 != nil || err2 != nil {
		return pvfversion.FileChange{}, false
	}
	disp := after.Path(ai)
	if disp == "" {
		disp = before.Path(bi)
	}
	if disp == "" {
		disp = displayFallback
	}
	if bType != aType {
		return pvfversion.FileChange{
			Path:        canonicalKey,
			DisplayPath: pvfversion.DisplayPath(disp),
			Operation:   pvfversion.OperationModify,
			BeforeType:  bType,
			AfterType:   aType,
		}, true
	}

	isText := bType == pvf.TypeScript || bType == pvf.TypeUnicode
	if !isText {
		if bSize != aSize {
			return pvfversion.FileChange{
				Path:        canonicalKey,
				DisplayPath: pvfversion.DisplayPath(disp),
				Operation:   pvfversion.OperationModify,
				BeforeType:  bType,
				AfterType:   aType,
			}, true
		}
		bRaw, err1 := before.RawBytes(bi)
		aRaw, err2 := after.RawBytes(ai)
		if err1 != nil || err2 != nil || !bytes.Equal(bRaw, aRaw) {
			return pvfversion.FileChange{
				Path:        canonicalKey,
				DisplayPath: pvfversion.DisplayPath(disp),
				Operation:   pvfversion.OperationModify,
				BeforeType:  bType,
				AfterType:   aType,
				BeforeHash:  archiveDiffContentHash(bType, bRaw),
				AfterHash:   archiveDiffContentHash(aType, aRaw),
			}, true
		}
		return pvfversion.FileChange{}, false
	}

	// 文本或脚本文件，比较反编译后的标准文本
	bRaw, err1 := before.RawBytes(bi)
	aRaw, err2 := after.RawBytes(ai)
	if err1 != nil || err2 != nil {
		return pvfversion.FileChange{}, false
	}
	bText, err1 := before.CanonicalTextFromRaw(bi, bRaw)
	aText, err2 := after.CanonicalTextFromRaw(ai, aRaw)
	if err1 != nil || err2 != nil || bText != aText {
		return pvfversion.FileChange{
			Path:        canonicalKey,
			DisplayPath: pvfversion.DisplayPath(disp),
			Operation:   pvfversion.OperationModify,
			BeforeType:  bType,
			AfterType:   aType,
			BeforeHash:  archiveDiffContentHash(bType, []byte(bText)),
			AfterHash:   archiveDiffContentHash(aType, []byte(aText)),
		}, true
	}

	return pvfversion.FileChange{}, false
}

// mergeDiffFileEntry 合并单个文件条目，跨归档文本文件使用反编译文本写入确保字符串表入池
func mergeDiffFileEntry(target, source *pvf.Archive, rawPath string, sourceIdx int32) (bool, error) {
	sourceType, _, err := source.FileContentInfo(sourceIdx)
	if err != nil {
		return false, err
	}

	canonicalKey := pvfversion.CanonicalPath(rawPath)
	targetIdx, hasTarget := target.Find(canonicalKey)

	// 文本文件：通过 CanonicalText 反编译获取真实文本，再 SetText 写入目标归档的字符串池
	if sourceType == pvf.TypeScript || sourceType == pvf.TypeUnicode {
		text, err := source.CanonicalText(sourceIdx)
		if err != nil {
			return false, fmt.Errorf("反编译源文本失败: %w", err)
		}
		if hasTarget {
			targetType, _, _ := target.FileContentInfo(targetIdx)
			if targetType != sourceType {
				_ = target.SetDataType(targetIdx, sourceType)
			}
			if err := target.SetText(targetIdx, text); err != nil {
				return false, err
			}
			return false, nil
		}
		_, err = target.AddFileText(rawPath, text, sourceType)
		return true, err
	}

	// 二进制文件：直接复制 RawBytes
	raw, err := source.RawBytes(sourceIdx)
	if err != nil {
		return false, fmt.Errorf("读取源二进制数据失败: %w", err)
	}
	if hasTarget {
		targetType, _, _ := target.FileContentInfo(targetIdx)
		if targetType != sourceType {
			_ = target.SetDataType(targetIdx, sourceType)
		}
		if err := target.SetRawBytes(targetIdx, raw); err != nil {
			return false, err
		}
		return false, nil
	}
	target.AddFile(rawPath, raw, sourceType)
	return true, nil
}

// deleteDiffFileEntry 从当前归档删除文件
func deleteDiffFileEntry(a *pvf.Archive, rawPath string) (bool, error) {
	canonicalKey := pvfversion.CanonicalPath(rawPath)
	idx, exists := a.Find(canonicalKey)
	if !exists {
		return false, nil
	}
	deleted, err := a.RemoveFiles([]int32{idx})
	if err != nil {
		return false, err
	}
	return len(deleted) > 0, nil
}

// expandSelectedLeafPaths 将选中的路径列表（包含目录路径或具体文件）展开为去重后的叶子差异文件路径
func expandSelectedLeafPaths(changes map[string]pvfversion.FileChange, paths []string) []string {
	if len(paths) == 0 || len(changes) == 0 {
		return nil
	}
	resultMap := make(map[string]string)

	for _, rawPath := range paths {
		cleaned := strings.TrimSpace(rawPath)
		if cleaned == "" {
			continue
		}
		canonical := pvfversion.CanonicalPath(cleaned)
		if change, exists := changes[canonical]; exists {
			resultMap[canonical] = change.DisplayPath
			continue
		}
		dirPrefix := canonical + "/"
		for key, change := range changes {
			if strings.HasPrefix(key, dirPrefix) {
				resultMap[key] = change.DisplayPath
			}
		}
	}

	leaves := make([]string, 0, len(resultMap))
	for _, display := range resultMap {
		leaves = append(leaves, display)
	}
	return leaves
}
