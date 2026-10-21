package services

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"pvfine/internal/importing"
	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

const (
	importCenterTTL = 10 * time.Minute
	// importCenterPageLimit 是一页返回的条目上限。200,000 个条目不可能一次
	// 塞进 Wails 的 JSON 通道，前端只能分页取。
	importCenterPageLimit = 200
	// 上限必须显式命中：静默截断会让用户以为全导进来了。
	importCenterMaxFiles = 200_000
	importCenterMaxBytes = 512 << 20
)

// ErrImportCenterStale reports a plan that was released, expired, or whose
// archive moved underneath it.
var ErrImportCenterStale = fmt.Errorf("导入预览已过期，请重新预览")

var importCenterSequence atomic.Uint64

// ImportCenterRequest identifies one import source. Only extraction products
// are supported in this version; a plain directory keeps using ImportFiles.
type ImportCenterRequest struct {
	Source string `json:"source"`
	Root   string `json:"root"`
}

// ImportCounts summarises a plan by state.
type ImportCounts struct {
	Total       int `json:"total"`
	New         int `json:"new"`
	Overwrite   int `json:"overwrite"`
	Unchanged   int `json:"unchanged"`
	Unsupported int `json:"unsupported"`
	Invalid     int `json:"invalid"`
	Selected    int `json:"selected"`
}

// ImportCenterPreview is the frozen plan's summary.
type ImportCenterPreview struct {
	ID       string           `json:"id"`
	Root     string           `json:"root"`
	Counts   ImportCounts     `json:"counts"`
	Lists    ImportListCounts `json:"lists"`
	Warnings []string         `json:"warnings"`
}

// ImportCenterEntry is one row of the preview table.
type ImportCenterEntry struct {
	ArchivePath   string `json:"archivePath"`
	Form          string `json:"form"`
	DataType      int32  `json:"dataType"`
	Size          int64  `json:"size"`
	Category      string `json:"category"`
	AutoCompleted bool   `json:"autoCompleted"`
	State         string `json:"state"`
	Selected      bool   `json:"selected"`
	Reason        string `json:"reason"`
}

// ImportCenterPage is one slice of the plan. Next < 0 means the list ended.
type ImportCenterPage struct {
	Entries []ImportCenterEntry `json:"entries"`
	Cursor  int                 `json:"cursor"`
	Next    int                 `json:"next"`
	Total   int                 `json:"total"`
}

// ImportCenterApplyResult reports what reached the archive.
type ImportCenterApplyResult struct {
	Written      int      `json:"written"`
	Overwritten  int      `json:"overwritten"`
	Skipped      int      `json:"skipped"`
	ListRows     int      `json:"listRows"`
	ListDangling int      `json:"listDangling"`
	ChangedPaths []string `json:"changedPaths,omitempty"`
}

type importCenterPlan struct {
	id       string
	created  time.Time
	root     string
	entries  []importing.FileEntry
	byPath   map[string]int
	warnings []string
	// 登记表一半：Open 时按「归档 + 本次候选路径」判定一次，Execute 直接复用。
	// 候选路径只是"将要写入"，所以预览期不需要真的把文件塞进克隆体。
	listEntries       []importing.ListEntry
	requiresIndexHash bool
	// canChangeStructure is captured at plan time so the error surfaces before
	// the user waits through a large apply.
	canChangeStructure bool

	archive  *pvf.Archive
	repo     *pvfversion.Repository
	revision uint64
	loadID   uint64
	headID   string

	// NPK 精简是懒加载的：只导入文本时不应该去扫 3000 个容器。
	npkPlanned    bool
	npkTargetDir  string
	npkEntries    []ImportNPKEntry
	npkContainers []importNPKContainer
	npkWarnings   []string
}

// ImportCenterService owns the import plan lifecycle. Every read of the live
// archive happens on a snapshot: opening or editing a plan must not touch the
// user's archive at all.
type ImportCenterService struct {
	c      *core
	mu     sync.Mutex
	plan   *importCenterPlan
	expiry *time.Timer
}

func NewImportCenterService(c *core) *ImportCenterService {
	return &ImportCenterService{c: c}
}

// Open parses an extraction product, classifies every entry against the current
// archive and freezes the result. Nothing is written: the live archive is only
// read.
func (s *ImportCenterService) Open(request ImportCenterRequest) (*ImportCenterPreview, error) {
	root := strings.TrimSpace(request.Root)
	if root == "" {
		return nil, fmt.Errorf("请选择提取产物目录")
	}
	if request.Source != "" && request.Source != "extraction" {
		return nil, fmt.Errorf("当前版本只支持导入提取产物")
	}

	finishTask := s.c.archiveTasks.begin()
	defer finishTask()

	source, err := importing.FromExtraction(root)
	if err != nil {
		return nil, err
	}
	if len(source.Candidates) > importCenterMaxFiles {
		return nil, fmt.Errorf("条目数 %d 超过上限 %d，请先缩小提取范围",
			len(source.Candidates), importCenterMaxFiles)
	}
	// 体积上限只看实际文件大小。manifest 里的 size 是产物自己写的，改大改小都
	// 不受控；用它做闸门等于把上限交给输入。
	var actual int64
	for _, candidate := range source.Candidates {
		info, err := os.Stat(candidate.SourcePath)
		if err != nil {
			// 缺文件的条目稍后会判 invalid，不该在这里整批失败。
			continue
		}
		actual += info.Size()
		if actual > importCenterMaxBytes {
			return nil, fmt.Errorf("产物实际体积超过上限 %d MiB", importCenterMaxBytes>>20)
		}
	}

	s.c.mu.RLock()
	archive := s.c.archive
	if archive == nil {
		s.c.mu.RUnlock()
		return nil, ErrNoArchive
	}
	entries, err := importing.Classify(archive, source)
	if err != nil {
		s.c.mu.RUnlock()
		return nil, err
	}
	// 登记表一半：片段来自产物，判定对象是「当前归档 + 本次候选路径」。候选路径只是
	// 将要写入，所以预览期不需要真的把文件塞进克隆体——那句话是 PlanListRows 的契约。
	listRows, fragmentErr := importing.ReadFragments(root, source.Manifest)
	warnings := []string{}
	if fragmentErr != nil {
		// 片段读不出来不该连累文件导入，但必须说出来：产物是不完整的。
		warnings = append(warnings, fmt.Sprintf("登记表片段读取失败，本次不导入登记行: %v", fragmentErr))
	}
	candidatePaths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.State == importing.FileNew || entry.State == importing.FileOverwrite {
			candidatePaths = append(candidatePaths, entry.ArchivePath)
		}
	}
	var listEntries []importing.ListEntry
	if len(listRows) > 0 {
		listEntries, err = importing.PlanListRows(archive, listRows, candidatePaths)
		if err != nil {
			s.c.mu.RUnlock()
			return nil, err
		}
	}
	repo, revision := s.c.versionRepo, s.c.batchRevision
	loadID, headID := s.c.versionLoadID, s.c.versionHead.ID
	canChangeStructure := archive.WriteCapabilities().CanChangeStructure
	requiresIndexHash := archive.ContentRules().RequiresIndexHash
	clientVersion := archive.ClientVersion()
	s.c.mu.RUnlock()

	plan := &importCenterPlan{
		id:                 fmt.Sprint(importCenterSequence.Add(1)),
		created:            time.Now(),
		root:               root,
		entries:            entries,
		byPath:             make(map[string]int, len(entries)),
		canChangeStructure: canChangeStructure,
		listEntries:        listEntries,
		requiresIndexHash:  requiresIndexHash,
		archive:            archive,
		repo:               repo,
		revision:           revision,
		loadID:             loadID,
		headID:             headID,
	}
	for index := range entries {
		plan.byPath[entries[index].ArchivePath] = index
	}
	plan.warnings = append(warnings, importCenterWarnings(source, entries, clientVersion, canChangeStructure)...)
	listCounts := plan.listCounts()
	if listCounts.Dangling > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"%d 个引用 ID 在目标表里仍然缺失，导入后这些引用会是断的", listCounts.Dangling))
	}
	if listCounts.NoTable > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"%d 张登记表在目标归档里不存在，相关行不会写入", listCounts.NoTable))
	}
	if listCounts.IDTaken > 0 || listCounts.Duplicate > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"%d 个 ID 已被占用、%d 个路径已被别的 ID 登记，需在登记表页逐条选择", listCounts.IDTaken, listCounts.Duplicate))
	}

	s.mu.Lock()
	if s.expiry != nil {
		s.expiry.Stop()
	}
	s.plan = plan
	s.expiry = time.AfterFunc(importCenterTTL, func() { s.Release(plan.id) })
	s.mu.Unlock()

	preview := plan.summary()
	return &preview, nil
}

// OpenDialog opens a native folder picker and freezes the chosen product.
// A cancelled dialog returns (nil, nil): the frontend must not treat it as an
// error.
func (s *ImportCenterService) OpenDialog() (*ImportCenterPreview, error) {
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(false).
		SetTitle("选择提取产物目录（含 manifest.json）").
		PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, nil
	}
	return s.Open(ImportCenterRequest{Source: "extraction", Root: dir})
}

// PickDirectory opens a native folder picker and returns the chosen path.
// An empty result means the user cancelled; callers must not treat it as an
// error. It exists so the NPK prune can pick an output directory without
// inventing a second dialog round-trip.
func (s *ImportCenterService) PickDirectory(title string) (string, error) {
	if strings.TrimSpace(title) == "" {
		title = "选择目录"
	}
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		SetTitle(title).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// Page returns one slice of the frozen plan.
func (s *ImportCenterService) Page(id string, cursor, limit int) (*ImportCenterPage, error) {
	s.mu.Lock()
	plan := s.plan
	s.mu.Unlock()
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	if limit <= 0 || limit > importCenterPageLimit {
		limit = importCenterPageLimit
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(plan.entries) {
		cursor = len(plan.entries)
	}
	end := cursor + limit
	if end > len(plan.entries) {
		end = len(plan.entries)
	}
	page := &ImportCenterPage{Total: len(plan.entries), Cursor: cursor, Next: end}
	if end >= len(plan.entries) {
		page.Next = -1
	}
	for _, entry := range plan.entries[cursor:end] {
		page.Entries = append(page.Entries, importCenterEntryView(entry))
	}
	return page, nil
}

// SetSelected flips the write flag on known entry paths and reports the new
// counts. Unknown paths are ignored rather than failing: a stale frontend page
// should not break editing.
func (s *ImportCenterService) SetSelected(id string, archivePaths []string, selected bool) (*ImportCenterPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plan
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	for _, archivePath := range archivePaths {
		index, ok := plan.byPath[archivePath]
		if !ok {
			continue
		}
		entry := &plan.entries[index]
		// Only writable states can be toggled; unchanged/unsupported/invalid have
		// nothing to apply and must not look selectable in the UI.
		if entry.State != importing.FileNew && entry.State != importing.FileOverwrite {
			continue
		}
		entry.Selected = selected
	}
	preview := plan.summary()
	return &preview, nil
}

// Execute applies the selected entries. It runs on a clone, so a failure leaves
// the live archive untouched.
func (s *ImportCenterService) Execute(id string) (*ImportCenterApplyResult, error) {
	finishTask := s.c.archiveTasks.begin()
	defer finishTask()

	s.mu.Lock()
	plan := s.plan
	if plan == nil || plan.id != id {
		s.mu.Unlock()
		return nil, ErrImportCenterStale
	}
	selected := plan.selectedWritable()
	newCount := plan.selectedNew()
	listPaths := plan.changedListPaths()
	listDangling := plan.listCounts().Dangling
	s.mu.Unlock()

	// 只勾登记行也是合法的:文件可能早就在归档里,只缺那一行。
	if len(selected) == 0 && len(listPaths) == 0 {
		return &ImportCenterApplyResult{Skipped: len(plan.entries), ListDangling: listDangling}, nil
	}

	s.c.mu.Lock()
	if !s.validPlanLocked(plan) {
		s.c.mu.Unlock()
		return nil, ErrImportCenterStale
	}
	// 新增文件与写入登记行都会往字符串池追加内容，属于结构性改动。
	if !plan.canChangeStructure && (newCount > 0 || plan.writesListPairs()) {
		s.c.mu.Unlock()
		return nil, fmt.Errorf("当前归档的哈希键未知，不能新增条目或登记行；请先补全 sk.dat/DNF.exe 后重试")
	}
	if err := s.c.ensureVersionReadyLocked(); err != nil {
		s.c.mu.Unlock()
		return nil, err
	}
	archive := s.c.archive
	if archive == nil {
		s.c.mu.Unlock()
		return nil, ErrNoArchive
	}

	paths := make([]string, 0, len(selected)+len(listPaths))
	for _, entry := range selected {
		paths = append(paths, entry.ArchivePath)
	}
	// 登记表路径必须一起进快照：漏了它们，撤销只会还原文件、表里的新行留着。
	// 这里在应用之前就要预测出会被改的表，因为 before 快照是此刻取的。
	paths = append(paths, listPaths...)
	revision := s.c.batchRevision
	repo, loadID, headID := s.c.versionRepo, s.c.versionLoadID, s.c.versionHead.ID
	incrementalSQLite := s.c.diskIndex != nil && s.c.diskIndex.entries != nil
	var beforeSnapshot pvfversion.ContentSnapshot
	if s.c.versionRepo != nil {
		snapshot, err := pvfversion.ContentSnapshotFromArchive(archive, paths)
		if err != nil {
			s.c.mu.Unlock()
			return nil, err
		}
		beforeSnapshot = snapshot
	}
	stage := archive.CloneForBatch()
	s.c.batchRevision++
	s.c.mu.Unlock()

	written, overwritten, err := applyImportCenterEntries(stage, selected)
	if err != nil {
		return nil, err
	}
	listChanged, err := importing.ApplyListRows(stage, plan.listEntries)
	if err != nil {
		return nil, err
	}
	// 预测与实际必须一致：少算一张表，撤销就会把表留在改过的状态。
	planned := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		planned[path] = struct{}{}
	}
	for _, path := range listChanged {
		if _, ok := planned[path]; !ok {
			return nil, fmt.Errorf("内部错误：登记表 %s 的改动没有进入版本快照", path)
		}
	}
	listRows := 0
	for _, entry := range plan.listEntries {
		if entry.Selected && entry.ListIndex >= 0 {
			listRows++
		}
	}
	result := &ImportCenterApplyResult{
		Written: written, Overwritten: overwritten,
		Skipped: len(plan.entries) - len(selected), ListRows: listRows,
		ListDangling: plan.listCounts().Dangling, ChangedPaths: paths,
	}
	if len(stage.MutationsSince(0).Files) == 0 {
		// An identical overwrite has no projection or semantic work to publish.
		return result, nil
	}

	var prepared *preparedArchiveIndexes
	if !incrementalSQLite {
		prepared, err = prepareArchiveDerivedIndexes(stage)
		if err != nil {
			return nil, err
		}
	}

	s.c.mu.Lock()
	if s.c.archive != archive || s.c.batchRevision != revision+1 ||
		s.c.versionRepo != repo || s.c.versionLoadID != loadID || s.c.versionHead.ID != headID {
		closePreparedArchiveIndexes(prepared)
		s.c.mu.Unlock()
		return nil, ErrBatchPlanStale
	}
	var after pvfversion.ContentSnapshot
	if s.c.versionRepo != nil {
		after, err = pvfversion.ContentSnapshotFromArchive(stage, paths)
		if err != nil {
			closePreparedArchiveIndexes(prepared)
			s.c.mu.Unlock()
			return nil, err
		}
	}
	if incrementalSQLite {
		err = s.c.rebuildArchiveIndexesLocked(stage)
	} else {
		err = s.c.installPreparedArchiveIndexesLocked(stage, prepared, true)
	}
	if err != nil {
		closePreparedArchiveIndexes(prepared)
		s.c.mu.Unlock()
		return nil, err
	}
	if s.c.versionRepo != nil {
		if err := s.c.recordVersionMutationLocked("导入中心", beforeSnapshot, after); err != nil {
			s.c.mu.Unlock()
			return nil, err
		}
	}
	// 改过登记表就必须让注册索引和编辑器缓存的表文本失效：否则文件树里"已注册到 X"
	// 还是旧的，表正在编辑器打开时用户看到旧文本，再保存一次会把新行覆盖掉。
	if len(listChanged) > 0 {
		for _, listPath := range listChanged {
			if strings.HasSuffix(strings.ToLower(listPath), "_indexhash.etc") {
				continue
			}
			listIndex, found := stage.Find(listPath)
			if !found {
				continue
			}
			hashIndex := int32(-1)
			if hashPath, ok := pvf.IndexHashCompanionPath(listPath); ok {
				if index, ok := stage.Find(hashPath); ok {
					hashIndex = index
				}
			}
			refreshRegistrationEditorTextLocked(s.c, stage, listIndex, hashIndex)
		}
		invalidateRegistrationIndexesLocked(s.c)
	}
	mutationSummary := stage.MutationsSince(0)
	stage.ClearMutations()
	info := stage.Info()
	versioned := s.c.versionRepo != nil
	s.c.mu.Unlock()

	if err := s.c.scheduleArchiveMutations(stage, mutationSummary); err != nil {
		return nil, err
	}
	emitEvent("archive:changed", info)
	emitEvent("archive:advanced-search-stale", map[string]any{"import": true})
	if versioned {
		emitVersionState(s.c, "files-imported")
	}

	s.mu.Lock()
	if s.plan == plan {
		s.plan = nil
		if s.expiry != nil {
			s.expiry.Stop()
			s.expiry = nil
		}
	}
	s.mu.Unlock()
	return result, nil
}

// Release drops a plan. Called by the TTL timer and by the frontend on close.
func (s *ImportCenterService) Release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil || s.plan.id != id {
		return
	}
	s.plan = nil
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
}

// validPlanLocked reports whether the frozen plan still describes the live
// archive. Caller holds c.mu.
func (s *ImportCenterService) validPlanLocked(plan *importCenterPlan) bool {
	if time.Since(plan.created) >= importCenterTTL {
		return false
	}
	return s.c.archive == plan.archive &&
		s.c.versionRepo == plan.repo &&
		s.c.batchRevision == plan.revision &&
		s.c.versionLoadID == plan.loadID &&
		s.c.versionHead.ID == plan.headID
}

func (p *importCenterPlan) selectedWritable() []importing.FileEntry {
	result := make([]importing.FileEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		if entry.Selected && (entry.State == importing.FileNew || entry.State == importing.FileOverwrite) {
			result = append(result, entry)
		}
	}
	return result
}

func (p *importCenterPlan) selectedNew() int {
	count := 0
	for _, entry := range p.entries {
		if entry.Selected && entry.State == importing.FileNew {
			count++
		}
	}
	return count
}

func (p *importCenterPlan) summary() ImportCenterPreview {
	preview := ImportCenterPreview{
		ID: p.id, Root: p.root, Warnings: p.warnings,
		Lists: p.listCounts(),
	}
	preview.Counts.Total = len(p.entries)
	for _, entry := range p.entries {
		switch entry.State {
		case importing.FileNew:
			preview.Counts.New++
		case importing.FileOverwrite:
			preview.Counts.Overwrite++
		case importing.FileUnchanged:
			preview.Counts.Unchanged++
		case importing.FileUnsupported:
			preview.Counts.Unsupported++
		case importing.FileInvalid:
			preview.Counts.Invalid++
		}
		if entry.Selected {
			preview.Counts.Selected++
		}
	}
	return preview
}

func importCenterEntryView(entry importing.FileEntry) ImportCenterEntry {
	return ImportCenterEntry{
		ArchivePath:   entry.ArchivePath,
		Form:          entry.Form,
		DataType:      entry.DataType,
		Size:          entry.Size,
		Category:      entry.Category,
		AutoCompleted: entry.AutoCompleted,
		State:         string(entry.State),
		Selected:      entry.Selected,
		Reason:        entry.Reason,
	}
}

// importCenterWarnings surfaces every reason the result could be incomplete.
// A silent partial import is worse than a refused one: the user cannot tell
// "nothing to do" from "half of it was dropped".
func importCenterWarnings(source *importing.Source, entries []importing.FileEntry,
	clientVersion string, canChangeStructure bool) []string {
	warnings := []string{}
	if source.Manifest != nil {
		if exported := strings.TrimSpace(source.Manifest.ClientVersion); exported != "" &&
			clientVersion != "" && exported != clientVersion {
			warnings = append(warnings, fmt.Sprintf(
				"这份产物来自客户端 %s，当前归档是 %s；跨版本导入的登记行可能无效", exported, clientVersion))
		}
	}
	var unsupported, invalid, writable int
	for _, entry := range entries {
		switch entry.State {
		case importing.FileUnsupported:
			unsupported++
		case importing.FileInvalid:
			invalid++
		case importing.FileNew, importing.FileOverwrite:
			writable++
		}
	}
	if unsupported > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d 个条目不在本次导入范围（二进制或登记表），不会被写入", unsupported))
	}
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d 个条目无法读取或编码非法，请检查产物是否完整", invalid))
	}
	if writable == 0 {
		warnings = append(warnings, "没有可写入的条目")
	}
	if !canChangeStructure {
		warnings = append(warnings, "当前归档的写入能力受限：可以覆盖，但不能新增条目")
	}
	return warnings
}

// applyImportCenterEntries writes the selected text entries into stage.
//
// The payload is re-read and its digest re-checked: a file edited after the
// preview must not be applied silently, because the registry judgement in the
// next phase depends on exactly this content.
func applyImportCenterEntries(stage *pvf.Archive, entries []importing.FileEntry) (int, int, error) {
	written, overwritten := 0, 0
	for _, entry := range entries {
		data, err := os.ReadFile(entry.SourcePath)
		if err != nil {
			return 0, 0, fmt.Errorf("读取 %s 失败: %w", entry.SourcePath, err)
		}
		data, err = importing.NormalizeTextPayload(data)
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", entry.ArchivePath, err)
		}
		if sha256.Sum256(data) != entry.Digest {
			return 0, 0, fmt.Errorf("源文件在预览之后被修改: %s（请重新预览）", entry.ArchivePath)
		}
		text := string(data)
		if index, exists := stage.Find(entry.ArchivePath); exists {
			if stage.File(index).DataType != entry.DataType {
				if err := stage.SetDataType(index, entry.DataType); err != nil {
					return 0, 0, fmt.Errorf("设置 %s 类型失败: %w", entry.ArchivePath, err)
				}
			}
			if err := stage.SetText(index, text); err != nil {
				return 0, 0, fmt.Errorf("写入 %s 失败: %w", entry.ArchivePath, err)
			}
			overwritten++
			continue
		}
		if _, err := stage.AddFileText(entry.ArchivePath, text, entry.DataType); err != nil {
			return 0, 0, fmt.Errorf("新增 %s 失败: %w", entry.ArchivePath, err)
		}
		written++
	}
	return written, overwritten, nil
}
