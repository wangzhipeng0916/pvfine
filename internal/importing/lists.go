package importing

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"pvfine/internal/pvf"
)

// ListState 是一行登记表条目的处置态。判定顺序见 PlanListRows：表 → ID →
// 路径 → 能否解析。顺序不能换，否则「ID 存在但目标文件已缺失」的陈旧行会退化成
// 「ID 不存在」，被当成 new 再追加一条同 ID 行。
type ListState string

const (
	// ListNew 表示这行可以写：ID 空闲，目标能解析。
	ListNew ListState = "new"
	// ListRegistered 表示目标表里已经是同一行，写入只会制造假变更。
	ListRegistered ListState = "registered"
	// ListIDTaken 表示该 ID 在表里已指向别的路径。
	ListIDTaken ListState = "id-taken"
	// ListDuplicate 表示该路径已被别的 ID 登记。
	ListDuplicate ListState = "duplicate"
	// ListDangling 表示行能读出来，但目标文件既不在归档里、也不在本次候选里。
	ListDangling ListState = "dangling"
	// ListNoTable 表示目标归档没有这张表。整表报告，绝不新建。
	ListNoTable ListState = "no-table"
)

// ListResolution 是冲突的消解方式。
type ListResolution string

const (
	// ResolveSkip 表示不处理该行（默认）。
	ResolveSkip ListResolution = "skip"
	// ResolveOverwrite 只用于 id-taken：把该 ID 改指向本行。
	ResolveOverwrite ListResolution = "overwrite"
	// ResolveRemoveOld 只用于 duplicate：删掉占用该路径的另一行。
	ResolveRemoveOld ListResolution = "remove-old"
)

// ListEntry 是一行登记表的判定结果。
type ListEntry struct {
	// ListPath 是归档里的真实表路径（表不存在时保留片段里的写法，便于报告）。
	ListPath  string
	ListIndex int32 // 表在归档里的索引;表不存在为 -1
	ID        string
	RowPath   string // fragment 原始写法
	// TargetPath 是按目标表布局重算后的写回形式;解析不到为空。
	// 预览期命中的候选文件（尚未进归档）同样换算成目标表的写法。
	TargetPath  string
	TargetIndex int32 // 解析到的归档文件索引;解析不到为 -1
	State       ListState
	Resolution  ListResolution
	Selected    bool
	ConflictOf  string // id-taken:该 ID 现在指向的路径;duplicate:已占用该路径的 ID(逗号分隔)
	Reason      string
}

// listRow 是目标表里已有的一行，书写路径与解析结果一起保存：只存索引会让
// 「ID 存在但指向缺失文件」退化成「ID 不存在」。
type listRow struct {
	ID        string
	RawPath   string // 表里的原始写法
	FileIndex int32  // 解析到的归档文件索引；解析不到 = -1
}

// listTable 是一张表的判定索引。ByPathIndex 与 ByRawPath 两个索引都必须有：
// 陈旧行解析不到文件索引，只看 ByPathIndex 会漏掉它们；而跨布局时同一文件的
// 写法又不同，只看原始路径会漏掉能解析的行。
type listTable struct {
	Index       int32
	ActualPath  string
	ByID        map[string]listRow
	ByPathIndex map[int32][]string  // 文件索引 → 占用它的 ID
	ByRawPath   map[string][]string // 规范化后的原始路径 → 占用它的 ID
}

// PlanListRows 逐行判定登记表片段，产出可编辑的计划。
//
// a 必须是「本次文件已经应用之后」的克隆体：候选文件只有进了归档，它的登记行
// 才通过 pvf.FindListTarget 解析得到。预览阶段文件还没有写进克隆体，所以调用方
// 把本次将要写入的候选路径（Candidate.ArchivePath）作为 candidatePaths 传进来：
// 按「小写 + 正斜杠」归一后命中候选集就视为可解析（TargetIndex 仍为 -1，因为
// 条目还不存在）；不传候选集的话，预览期所有实体行都会变成 dangling，这个功能
// 就没用了。
//
// 同一张表只建一次索引并缓存：大表有几十万行，逐行 FindList + ListPairs 会把
// 载荷反复解码。
func PlanListRows(a *pvf.Archive, rows []FragmentRow, candidatePaths []string) ([]ListEntry, error) {
	if a == nil {
		return nil, errors.New("导入中心: 目标归档为空")
	}
	candidates := make(map[string]string, len(candidatePaths))
	for _, candidate := range candidatePaths {
		candidate = strings.Trim(strings.ReplaceAll(strings.TrimSpace(candidate), "\\", "/"), "/")
		normalized := normalizeListPath(candidate)
		if normalized == "" {
			continue
		}
		if _, exists := candidates[normalized]; !exists {
			candidates[normalized] = candidate
		}
	}

	tables := make(map[string]*listTable)
	entries := make([]ListEntry, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		rawPath := strings.TrimSpace(row.Path)
		if id == "" || rawPath == "" {
			return nil, fmt.Errorf("登记表行缺少 id 或路径: id=%q path=%q", row.ID, row.Path)
		}
		// 同一 (表, id) 的重复片段行只保留第一条：提取侧可能把同一行同时写进
		// 「被引用」与「实体自身」两段，重复写入会让同一 ID 在表里出现两次。
		dedupeKey := normalizeListPath(row.ListPath) + "\x00" + id
		if _, duplicate := seen[dedupeKey]; duplicate {
			continue
		}
		seen[dedupeKey] = struct{}{}

		tableKey := normalizeListPath(row.ListPath)
		table, cached := tables[tableKey]
		if !cached {
			loaded, err := loadListTable(a, row.ListPath)
			if err != nil {
				return nil, err
			}
			tables[tableKey] = loaded
			table = loaded
		}
		entries = append(entries, judgeListRow(a, table, row.ListPath, id, rawPath, candidates))
	}
	return entries, nil
}

func loadListTable(a *pvf.Archive, listPath string) (*listTable, error) {
	table := &listTable{Index: -1}
	// 表定位一律用 FindList：它覆盖 90（`equipment/equipment.lst`）与 110
	// （`list/equipment.lst`）两套布局，自己拼 `list/` 只会在旧归档上失效。
	index, ok := a.FindList(listPath)
	if !ok {
		return table, nil
	}
	table.Index = index
	table.ActualPath = a.Path(index)

	pairs, err := a.ListPairs(index)
	if err != nil {
		return nil, fmt.Errorf("读取登记表 %s 失败: %w", table.ActualPath, err)
	}
	table.ByID = make(map[string]listRow, len(pairs))
	table.ByPathIndex = make(map[int32][]string, len(pairs))
	table.ByRawPath = make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		id := strings.TrimSpace(pair.ID)
		rawPath := strings.TrimSpace(pair.Path)
		if id == "" || rawPath == "" {
			continue
		}
		row := listRow{ID: id, RawPath: rawPath, FileIndex: -1}
		if _, fileIndex, resolved := pvf.FindListTarget(a, table.ActualPath, rawPath); resolved {
			row.FileIndex = fileIndex
		}
		if _, exists := table.ByID[id]; !exists {
			table.ByID[id] = row
		}
		if row.FileIndex >= 0 {
			table.ByPathIndex[row.FileIndex] = appendID(table.ByPathIndex[row.FileIndex], id)
		}
		normalized := normalizeListPath(rawPath)
		table.ByRawPath[normalized] = appendID(table.ByRawPath[normalized], id)
	}
	return table, nil
}

// judgeListRow 的判定顺序与设计 §4.2 一致，不能调换：先 ID、再路径、最后才
// 判断能否解析。反过来做的话，「ID 已存在但文件缺失」会被当成 ID 空闲。
func judgeListRow(a *pvf.Archive, table *listTable, requestedListPath, id, rawPath string,
	candidates map[string]string) ListEntry {
	entry := ListEntry{
		ListPath:    requestedListPath,
		ListIndex:   -1,
		ID:          id,
		RowPath:     rawPath,
		TargetIndex: -1,
	}
	if table.Index < 0 {
		entry.State = ListNoTable
		entry.Reason = "归档里没有这张登记表"
		return entry
	}
	entry.ListPath = table.ActualPath
	entry.ListIndex = table.Index

	// 目标解析：先按真实归档查，再退回本次候选集（预览期文件还没进归档）。
	// 写回路径必须重算，不能照抄片段里的写法：源归档可能是 90 布局、目标是 110。
	targetPath := ""
	if resolvedPath, fileIndex, resolved := pvf.FindListTarget(a, table.ActualPath, rawPath); resolved {
		entry.TargetIndex = fileIndex
		targetPath = resolvedPath
	} else if candidate, ok := matchListCandidate(table.ActualPath, rawPath, candidates); ok {
		targetPath = candidate
	}
	if targetPath != "" {
		entry.TargetPath = pvf.ListEntryPath(table.ActualPath, targetPath)
	}

	if existing, exists := table.ByID[id]; exists {
		if sameListRow(existing, entry.TargetIndex, rawPath) {
			entry.State = ListRegistered
			entry.Reason = "表里已经是这一行"
			return entry
		}
		entry.State = ListIDTaken
		entry.ConflictOf = existing.RawPath
		entry.Resolution = ResolveSkip
		entry.Reason = "该 ID 已指向别的路径"
		return entry
	}

	owners := make([]string, 0, 2)
	seenOwner := make(map[string]struct{}, 2)
	collect := func(ids []string) {
		for _, owner := range ids {
			if owner == id {
				continue
			}
			if _, exists := seenOwner[owner]; exists {
				continue
			}
			seenOwner[owner] = struct{}{}
			owners = append(owners, owner)
		}
	}
	if entry.TargetIndex >= 0 {
		collect(table.ByPathIndex[entry.TargetIndex])
	}
	collect(table.ByRawPath[normalizeListPath(rawPath)])
	if entry.TargetPath != "" {
		// 片段行是「表内写法」，候选是「归档路径」，跨布局时两者不同；先统一到表内
		// 写法再比，否则 90 表里的同一行会被当成两个不同路径。
		collect(table.ByRawPath[normalizeListPath(entry.TargetPath)])
	}
	if len(owners) > 0 {
		sort.Strings(owners)
		entry.State = ListDuplicate
		entry.ConflictOf = strings.Join(owners, ",")
		entry.Resolution = ResolveSkip
		entry.Reason = "该路径已被别的 ID 登记"
		return entry
	}

	if entry.TargetIndex < 0 && entry.TargetPath == "" {
		entry.State = ListDangling
		entry.Reason = "目标文件不在归档中，也不在本次导入候选中"
		return entry
	}

	entry.State = ListNew
	entry.Selected = true
	entry.Resolution = ResolveSkip
	if entry.TargetIndex >= 0 {
		entry.Reason = "新增登记"
	} else {
		// 预览期命中的候选：文件写进归档后这一行才真正生效。
		entry.Reason = "候选文件写入后登记"
	}
	return entry
}

// matchListCandidate 把「表内写法」的片段行对到「归档路径」的候选集上。
//
// 90 布局的表把路径写成相对表目录（`new.equ`），而候选集里存的是归档路径
// （`equipment/new.equ`）；直接拿原始写法查候选集永远命不中，实体行会全部退化成
// dangling。这里用内核的多候选解析把两边统一。
func matchListCandidate(listPath, rawPath string, candidates map[string]string) (string, bool) {
	if candidate, ok := candidates[normalizeListPath(rawPath)]; ok {
		return candidate, true
	}
	paths, ok := pvf.ListPathCandidates(listPath, rawPath)
	if !ok {
		return "", false
	}
	for _, candidate := range paths {
		if found, ok := candidates[normalizeListPath(candidate)]; ok {
			return found, true
		}
	}
	return "", false
}

// sameListRow 判断表里已有的行与片段行是不是同一行。两边都解析到归档条目时
// 比索引（跨 90/110 写法也能对上）；有一边解析不到时退回比较规范化后的书写
// 路径——陈旧行没有索引可比，退回路径是唯一还能分辨「同一条陈旧行」的依据。
func sameListRow(existing listRow, targetIndex int32, rawPath string) bool {
	if existing.FileIndex >= 0 && targetIndex >= 0 {
		return existing.FileIndex == targetIndex
	}
	return normalizeListPath(existing.RawPath) == normalizeListPath(rawPath)
}

// normalizeListPath 统一小写与分隔符，并清掉 `./` 这类无意义片段。归档查找
// 本身是大小写不敏感的，判定必须同口径，否则同一个文件会因写法不同被判冲突。
func normalizeListPath(p string) string {
	p = strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "/")
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return strings.ToLower(p)
}

func appendID(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

// ApplyListRows 只处理 Selected 的行，把结果写回目标表。
//
// 返回值是改过的路径（含配套的 `_indexhash.etc`），供上层做版本快照与 undo：
// 漏掉表路径的后果不是报错，是撤销只还原文件、表里新行留着。
func ApplyListRows(a *pvf.Archive, entries []ListEntry) ([]string, error) {
	if a == nil {
		return nil, errors.New("导入中心: 目标归档为空")
	}

	type tableWork struct {
		pairs     []pvf.ListPair
		removeIDs []string
	}
	work := make(map[int32]*tableWork)
	for _, entry := range entries {
		if !entry.Selected || entry.ListIndex < 0 {
			continue
		}
		switch entry.State {
		case ListNew:
			// 新增行一律写。
		case ListIDTaken:
			// 冲突必须由用户显式选择，SetListPairs 的静默 upsert 不能替用户决定。
			if entry.Resolution != ResolveOverwrite {
				continue
			}
		case ListDuplicate:
			if entry.Resolution != ResolveRemoveOld {
				continue
			}
			if work[entry.ListIndex] == nil {
				work[entry.ListIndex] = &tableWork{}
			}
			for _, id := range strings.Split(entry.ConflictOf, ",") {
				if id = strings.TrimSpace(id); id != "" {
					work[entry.ListIndex].removeIDs = appendID(work[entry.ListIndex].removeIDs, id)
				}
			}
			// 删掉旧 ID 之后这条路径就空了，本行仍要写进去；所以这里不 continue，
			// 继续走下面的 pair 收集。
		default:
			continue
		}
		writePath := listWritePath(a, entry)
		if writePath == "" {
			return nil, fmt.Errorf("登记行 %s（%s）没有可写入的目标路径", entry.ID, entry.ListPath)
		}
		if work[entry.ListIndex] == nil {
			work[entry.ListIndex] = &tableWork{}
		}
		work[entry.ListIndex].pairs = append(work[entry.ListIndex].pairs,
			pvf.ListPair{ID: entry.ID, Path: writePath})
	}

	indexes := make([]int32, 0, len(work))
	for index := range work {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })

	changed := make([]string, 0, len(indexes)*2)
	seenChanged := make(map[string]struct{}, len(indexes)*2)
	addChanged := func(changedPath string) {
		if changedPath == "" {
			return
		}
		if _, exists := seenChanged[changedPath]; exists {
			return
		}
		seenChanged[changedPath] = struct{}{}
		changed = append(changed, changedPath)
	}

	for _, index := range indexes {
		job := work[index]
		if len(job.pairs) == 0 && len(job.removeIDs) == 0 {
			continue
		}
		actualListPath := a.Path(index)
		// 先删旧行再加新行：duplicate 的消解必须在 upsert 之前发生。
		if len(job.removeIDs) > 0 {
			if _, err := a.RemoveListIDs(index, job.removeIDs); err != nil {
				return nil, fmt.Errorf("从 %s 删除旧行失败: %w", actualListPath, err)
			}
		}
		if len(job.pairs) > 0 {
			if err := a.SetListPairs(index, job.pairs); err != nil {
				return nil, fmt.Errorf("写入 %s 失败: %w", actualListPath, err)
			}
		}
		addChanged(actualListPath)

		if !a.ContentRules().RequiresIndexHash {
			continue
		}
		// 110 的 indexhash 是表的配套文件：缺了它不能静默跳过，否则表里有了行、
		// 客户端仍按旧索引加载。
		hashPath, ok := pvf.IndexHashCompanionPath(actualListPath)
		if !ok {
			return nil, fmt.Errorf("无法确定 %s 的 indexhash 文件", actualListPath)
		}
		if _, found := a.Find(hashPath); !found {
			return nil, fmt.Errorf("indexhash 不存在: %s", hashPath)
		}
		ids := make([]uint32, 0, len(job.pairs))
		for _, pair := range job.pairs {
			parsed, err := strconv.ParseUint(strings.TrimSpace(pair.ID), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("此归档的 indexhash id 必须是 uint32 数字: %q", pair.ID)
			}
			ids = append(ids, uint32(parsed))
		}
		if len(ids) > 0 {
			if err := a.SetIndexHashEntriesForListIDs(hashPath, actualListPath, ids); err != nil {
				return nil, fmt.Errorf("更新 %s 失败: %w", hashPath, err)
			}
		}
		addChanged(hashPath)
	}
	return changed, nil
}

// listWritePath 计算一行最终写进表里的路径写法。
//
// 解析到归档条目时按该条目重算（a.Path(TargetIndex)），而不是照抄计划里的值：
// 计划可能是在另一个克隆体上做的，重算才是「按目标布局写」的唯一保证。预览期
// 命中的候选没有索引，TargetPath 已经是归档路径，只需换算成目标表的相对写法。
func listWritePath(a *pvf.Archive, entry ListEntry) string {
	actualListPath := entry.ListPath
	if entry.ListIndex >= 0 && entry.ListIndex < a.Info().FileCount {
		actualListPath = a.Path(entry.ListIndex)
	}
	if entry.TargetIndex >= 0 && entry.TargetIndex < a.Info().FileCount {
		return pvf.ListEntryPath(actualListPath, a.Path(entry.TargetIndex))
	}
	if entry.TargetPath == "" {
		return ""
	}
	return pvf.ListEntryPath(actualListPath, entry.TargetPath)
}
