package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Presets lists the named starting points the shipped preset document declares,
// validated against the catalog so a preset cannot name a category that no
// longer exists.
func (s *ExtractionService) Presets() ([]ExtractionPreset, error) {
	presets, err := shippedPresets()
	if err != nil {
		return nil, err
	}
	out := make([]ExtractionPreset, 0, len(presets))
	for _, preset := range presets {
		out = append(out, ExtractionPreset{ID: preset.ID, Name: preset.Name,
			Description: preset.Description, SeedHint: preset.SeedHint,
			Categories: append([]string(nil), preset.Categories...)})
	}
	return out, nil
}

// shippedPresets loads the embedded preset document against the embedded catalog.
func shippedPresets() ([]extraction.Preset, error) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		return nil, err
	}
	return extraction.LoadPresets(extraction.EmbeddedPresets, catalog)
}

// ErrExtractionStale reports that a preview was invalidated by an archive
// change or by age, exactly like an export preview.
var ErrExtractionStale = errors.New("提取预览已过期，请重新准备")

// ErrNoExtractionPlan reports an unknown plan id.
var ErrNoExtractionPlan = errors.New("提取预览不存在")

// planSequence numbers extraction previews so an expired id is never reused.
var planSequence atomic.Uint64

// extractionPlanTTL bounds how long a preview stays executable.
const extractionPlanTTL = 10 * time.Minute

// ExtractionCategory is one declared export category.
type ExtractionCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Order int    `json:"order"`
}

// ExtractionPreset is one named starting point. It pre-selects categories and
// says how to write a seed; it does not change how the closure is walked.
type ExtractionPreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SeedHint    string   `json:"seedHint"`
	Categories  []string `json:"categories,omitempty"`
}

// ExtractionRequest is one preview request: which entries, and how to organise
// them. Everything the UI shows comes from the returned preview, so the
// frontend never re-derives classification or completion itself.
type ExtractionRequest struct {
	Seeds []string `json:"seeds"`
	// ListRelation names the registry a pasted id list belongs to. It only
	// applies to bare numeric seeds: with a declared table there is nothing to
	// guess, so those resolve through it instead of the ambiguity guard.
	ListRelation string `json:"listRelation,omitempty"`
	// Preset applies the shipped preset's default categories when the caller left
	// categories empty. It never narrows the closure.
	Preset     string   `json:"preset,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Strict     bool     `json:"strict,omitempty"`
	Layout     string   `json:"layout,omitempty"`
	Form       string   `json:"form,omitempty"`
	MaxEntries int      `json:"maxEntries,omitempty"`
	// LstSuggest asks for registry registration proposals. Off by default: a
	// registration changes what the game loads, so it must be requested.
	LstSuggest bool `json:"lstSuggest,omitempty"`
	// CopyAssets additionally copies the NPK containers the exported set needs
	// into the package's npk/ folder, so one run yields scripts + artwork +
	// registration rows. Off by default because the volume is the expensive
	// part: one dungeon touches 15 containers / 100.7 MiB on the measured
	// archive, and the preview reports that size before anything is written.
	CopyAssets bool `json:"copyAssets,omitempty"`
}

// ExtractionCategoryPreview is one row of the preview table.
type ExtractionCategoryPreview struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Files     int      `json:"files"`
	Bytes     int64    `json:"bytes"`
	Refs      int      `json:"refs"`
	AutoFiles int      `json:"autoFiles"`
	Samples   []string `json:"samples,omitempty"`
}

// ExtractionPreview answers "what would this export contain" without touching
// the disk. The counts are the contract: main + ancestors + auto-completed is
// what lands, and strict mode reports its dangling references instead.
type ExtractionPreview struct {
	ID            string                      `json:"id"`
	Categories    []ExtractionCategoryPreview `json:"categories"`
	ClosureCount  int                         `json:"closureCount"`
	MainCount     int                         `json:"mainCount"`
	AncestorCount int                         `json:"ancestorCount"`
	ExternalCount int                         `json:"externalCount"`
	DanglingCount int                         `json:"danglingCount"`
	ExportedCount int                         `json:"exportedCount"`
	States        map[string]int              `json:"states"`
	VocabCoverage float64                     `json:"vocabCoverage"`
	// AssetTotal counts distinct client images the exported set references,
	// AssetMatched how many of them can be attributed to an NPK container, and
	// AssetDynamic how many are templates that only the running client can
	// resolve. They are preview fields on purpose: artwork is half of what makes
	// an equipment package usable, so the user sees it before choosing a folder.
	AssetTotal   int `json:"assetTotal"`
	AssetMatched int `json:"assetNpkMatched"`
	AssetDynamic int `json:"assetDynamic"`
	// AssetContainers and AssetBytes say what "把图片容器也拷进包" would cost, so
	// the choice is made on the preview instead of after a 100 MiB surprise.
	AssetContainers []string `json:"assetContainers,omitempty"`
	AssetBytes      int64    `json:"assetNpkBytes"`
	// Preset echoes the preset the category defaults came from, so the interface
	// can show "按装备预设" instead of leaving the user guessing why boxes moved.
	Preset   string   `json:"preset,omitempty"`
	Strict   bool     `json:"strict"`
	Layout   string   `json:"layout"`
	Warnings []string `json:"warnings,omitempty"`
}

// ExtractionResult reports what landed on disk.
type ExtractionResult struct {
	Dir           string `json:"dir"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	AutoCompleted int    `json:"autoCompleted"`
	Dangling      int    `json:"dangling"`
	// LstSuggestions lists registry additions that were requested and found.
	LstSuggestions []extraction.LstSuggestion `json:"lstSuggestions,omitempty"`
	// RegistryFragments are the _registries/ files holding the .lst rows this
	// extraction involved.
	RegistryFragments []string `json:"registryFragments,omitempty"`
	// AssetTotal and AssetMatched repeat the preview's resource counts as of what
	// actually landed, and AssetSheet names the cross-reference file in report/.
	AssetTotal   int    `json:"assetTotal"`
	AssetMatched int    `json:"assetNpkMatched"`
	AssetDynamic int    `json:"assetDynamic"`
	AssetSheet   string `json:"assetSheet,omitempty"`
	// AssetContainers names the distinct NPK files the matched images live in,
	// so "which containers to copy" is answerable without opening the sheet.
	AssetContainers []string `json:"assetContainers,omitempty"`
	// AssetCopied and AssetCopiedBytes report the opt-in container copy.
	AssetCopied      int   `json:"assetCopied"`
	AssetCopiedBytes int64 `json:"assetCopiedBytes"`
}

type extractionPlan struct {
	preview ExtractionPreview
	request ExtractionRequest
	plan    *extraction.Plan
	closure *extraction.Closure
	// scanner is the warmed instance the closure was walked with, so the export
	// and its fragments resolve against exactly the tables the preview showed.
	scanner *extraction.Scanner
	archive *pvf.Archive
	// generation is bumped whenever entries are added, removed or the archive is
	// reinstalled, so it covers everything a path-based lookup could depend on.
	generation uint64
	// watch covers the content a preview was computed from: the closure entries
	// plus every registry list. An in-place edit changes neither the archive
	// pointer nor the generation, so this is what makes it observable.
	watch    extraction.Watch
	watchSet []int32
	created  time.Time
}

// ExtractionService owns one short-lived immutable preview. It is the only
// place that merges user-visible rule overrides into the scanner: relations
// come from the same rule store the annotation engine uses, so an override
// applies to annotation jumps and to extraction at once.
type ExtractionService struct {
	c      *core
	mu     sync.Mutex
	plan   *extractionPlan
	expiry *time.Timer
	// The scanner cache is shared with MOD export on purpose: the key rules
	// (archive identity, generation, rule-engine identity, registry content
	// witness) must not drift into two slightly different definitions.
	scanners scannerCache
	// assets caches the persisted NPK image index for the configured directory.
	// Reading a persisted snapshot is cheap; scanning thousands of containers is
	// the ImageService's job at startup, so extraction never triggers it.
	assets    *imageSnapshot
	assetsDir string
	// images is the running image index when the app has one; it is preferred
	// over the persisted snapshot because it reflects an index built in this
	// session. Optional so the service keeps working when it is not wired up.
	images *ImageService
}

// NewExtractionService builds the service around the shared archive state. The
// optional image service answers "which NPK holds this image"; passing it is how
// the app connects extraction's resource sheet to the index the user configured.
func NewExtractionService(c *core, images ...*ImageService) *ExtractionService {
	service := &ExtractionService{c: c}
	if len(images) > 0 {
		service.images = images[0]
	}
	return service
}

// Categories lists the export categories declared by the shipped edge catalog.
func (s *ExtractionService) Categories() ([]ExtractionCategory, error) {
	catalog, err := extraction.LoadCatalog(extraction.EmbeddedEdges)
	if err != nil {
		return nil, err
	}
	out := make([]ExtractionCategory, 0, len(catalog.Categories()))
	for _, cat := range catalog.Categories() {
		out = append(out, ExtractionCategory{ID: cat.ID, Name: cat.Name, Order: cat.Order})
	}
	return out, nil
}

// summarizeSeedFailures keeps the all-failed error bounded while still naming
// what went wrong: a pasted list can be thousands of lines, and the message has
// to stay readable in the panel.
func summarizeSeedFailures(warnings []string) string {
	const shown = 3
	head := warnings
	if len(head) > shown {
		head = head[:shown]
	}
	message := strings.Join(head, "；")
	if len(warnings) > shown {
		message += fmt.Sprintf("；…共 %d 条解析失败", len(warnings))
	}
	return message
}

// ExtractionRelation is one registry the panel can declare as the table a
// pasted id list belongs to.
type ExtractionRelation struct {
	Name     string `json:"name"`
	Rows     int    `json:"rows"`
	ListPath string `json:"listPath,omitempty"`
}

// Relations lists the declared registries with the row count this archive
// actually has, so the panel can say "装备 119,532 行" instead of asking the user
// to remember the exact table name. A table whose list file is absent from the
// archive reports -1 rows rather than 0, because "0 行" and "这张表在这份归档里
// 不存在" must not look the same.
func (s *ExtractionService) Relations() ([]ExtractionRelation, error) {
	archive, generation, rules := s.currentArchive()
	if archive == nil {
		return nil, ErrNoArchive
	}
	scanner, err := s.scannerFor(archive, generation, rules)
	if err != nil {
		return nil, err
	}
	registers := scanner.RegistrySet()
	names := registers.Relations()
	out := make([]ExtractionRelation, 0, len(names))
	for _, name := range names {
		relation := ExtractionRelation{Name: name, Rows: registers.Rows(name)}
		if listPath, err := registers.ListPath(name); err == nil {
			relation.ListPath = listPath
		}
		out = append(out, relation)
	}
	return out, nil
}

// Prepare computes the closure, classifies it and applies the selection. The
// result is cached under an id so Execute does not recompute anything.
func (s *ExtractionService) Prepare(request ExtractionRequest) (*ExtractionPreview, error) {
	if len(request.Seeds) == 0 {
		return nil, fmt.Errorf("提取需要至少一个种子")
	}
	presetUsed := ""
	if request.Preset != "" {
		presets, err := shippedPresets()
		if err != nil {
			return nil, err
		}
		preset := extraction.FindPreset(presets, request.Preset)
		if preset == nil {
			// 认不出来的预设要报错：当成"没有预设"会让用户以为勾的是装备的类别。
			return nil, fmt.Errorf("未知的提取预设：%s", request.Preset)
		}
		presetUsed = preset.ID
		if len(request.Categories) == 0 {
			request.Categories = append([]string(nil), preset.Categories...)
		}
	}
	archive, generation, rules := s.currentArchive()
	if archive == nil {
		return nil, ErrNoArchive
	}
	scanner, err := s.scannerFor(archive, generation, rules)
	if err != nil {
		return nil, err
	}
	indexes := make([]int32, 0, len(request.Seeds))
	warnings := []string{}
	for _, seed := range request.Seeds {
		hit, err := extraction.ResolveSeedIn(archive, scanner.RegistrySet(), seed, request.ListRelation)
		if err != nil {
			warnings = append(warnings, "种子无法解析："+err.Error())
			continue
		}
		indexes = append(indexes, hit.Index)
	}
	if len(indexes) == 0 {
		// 一条都没解析出来时要把原因带上：贴了一整张清单却全错（比如表选错了）
		// 的时候，"没有可用的种子"本身没有告诉用户哪里错了。
		return nil, fmt.Errorf("没有可用的种子：%s", summarizeSeedFailures(warnings))
	}
	closure, err := scanner.Closure(indexes, extraction.Options{
		MaxEntries: request.MaxEntries, CollectUnmodeled: true,
	})
	if err != nil {
		return nil, err
	}
	if closure.Truncated {
		// 上限命中后返回的闭包与完整闭包形状相同，所以"这份预览只是样本"必须
		// 由服务端说出来，而不是等用户在导出后才发现少了文件。
		warnings = append(warnings, fmt.Sprintf(
			"闭包在 %d 条目上限处停止，仍有 %d 个可达条目未展开；下面的计数只是样本",
			request.MaxEntries, closure.Dropped))
	}
	plan, err := extraction.BuildPlan(archive, scanner.Catalog(), closure, extraction.Selection{
		Categories: request.Categories, Strict: request.Strict,
		Layout: request.Layout, Form: request.Form,
	})
	if err != nil {
		return nil, err
	}
	watchSet := s.witnessSet(closure)
	watch := contentWitness(s.c, archive, watchSet)
	preview := ExtractionPreview{
		ID:            nextPlanID(),
		ClosureCount:  len(closure.Nodes),
		MainCount:     len(plan.Main),
		AncestorCount: len(plan.Ancestors),
		ExternalCount: len(plan.External),
		DanglingCount: len(plan.Dangling),
		ExportedCount: plan.ExportedCount(),
		States:        map[string]int{},
		VocabCoverage: closure.VocabCoverage(),
		Strict:        request.Strict,
		Layout:        request.Layout,
		Preset:        presetUsed,
		Warnings:      warnings,
	}
	for state, count := range closure.States {
		preview.States[state.String()] = count
	}
	for _, row := range plan.Categories {
		preview.Categories = append(preview.Categories, ExtractionCategoryPreview{
			ID: row.ID, Name: row.Name, Files: row.Files, Bytes: row.Bytes,
			Refs: row.Refs, AutoFiles: row.AutoFiles, Samples: row.Samples,
		})
		if row.ID == extraction.OtherCategory && row.Files > 0 {
			preview.Warnings = append(preview.Warnings,
				fmt.Sprintf("有 %d 个条目未归入任何类别，分类表需要补声明", row.Files))
		}
	}
	if !request.Strict && len(plan.External) > 0 {
		preview.Warnings = append(preview.Warnings,
			fmt.Sprintf("勾选之外另有 %d 个条目仅为保持闭包而落盘，它们在 _external/ 下", len(plan.External)))
	}
	// 资源盘点在预览期就做：装备/时装这类内容的可用性取决于图片在不在，
	// 用户要在选目录之前看见"图片能不能落到 NPK"，而不是导出完才发现。
	locator, npkDirectory := s.assetLocator()
	inventory, inventoryErr := extraction.BuildAssetInventory(archive, scanner, plan.Exported(), locator)
	if inventoryErr != nil {
		warnings = append(warnings, "资源盘点失败："+inventoryErr.Error())
		preview.Warnings = warnings
	} else {
		preview.AssetTotal = inventory.Total
		preview.AssetMatched = inventory.Matched
		preview.AssetDynamic = inventory.Dynamic
		preview.AssetContainers = assetContainers(inventory)
		preview.AssetBytes = projectAssetBytes(npkDirectory, preview.AssetContainers)
		switch {
		case inventory.Total == 0:
		case locator == nil:
			preview.Warnings = append(preview.Warnings, fmt.Sprintf(
				"产物引用了 %d 个客户端图片，但没有可用的 NPK 索引（未选择 NPK 目录，或索引还没建好），对照表只能列路径给不出归属", inventory.Total))
		case inventory.Unmatched > 0:
			preview.Warnings = append(preview.Warnings, fmt.Sprintf(
				"%d 个图片在 NPK 索引（%s）里找不到，%d 个是 %%/通配模板只能运行时判定；两者都不算缺失，也不能当已归属",
				inventory.Unmatched, npkDirectory, inventory.Dynamic))
		}
	}

	s.mu.Lock()
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	s.plan = &extractionPlan{preview: preview, request: request, plan: plan,
		closure: closure, scanner: scanner, archive: archive, generation: generation,
		watch: watch, watchSet: watchSet, created: time.Now()}
	s.expiry = time.AfterFunc(extractionPlanTTL, func() { s.Release(preview.ID) })
	s.mu.Unlock()
	return &preview, nil
}

// assetLocator answers "which NPK container holds this image path?". The answer
// lives outside the archive. The running ImageService is preferred because it
// holds the index built in this session; with none, the snapshot persisted for
// the configured directory is read instead. Reading a persisted file is cheap,
// but building an index walks thousands of containers — that is ImageService's
// job at startup, and an export must not quietly start it. The second return
// value is the directory the answer came from.
func (s *ExtractionService) assetLocator() (extraction.AssetLocator, string) {
	if s.images != nil {
		if status := s.images.IndexStatus(); status.State == ImageIndexStateReady {
			indexer := s.images
			return indexer.locateNPK, status.Directory
		}
	}
	if s.c == nil || s.c.settings == nil {
		return nil, ""
	}
	settings, err := s.c.settings.GetSettings()
	if err != nil || settings.NPKDirectory == "" {
		return nil, ""
	}
	directory, err := canonicalImageDirectory(settings.NPKDirectory)
	if err != nil {
		return nil, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assets == nil || s.assetsDir != directory {
		snapshot, err := loadImageIndexCacheAt(directory, "")
		if err != nil {
			s.assets, s.assetsDir = nil, ""
			return nil, directory
		}
		s.assets, s.assetsDir = snapshot, directory
	}
	snapshot := s.assets
	return func(imagePath string) (string, bool) {
		normalized := normalizeImagePath(imagePath)
		if normalized == "" {
			return "", false
		}
		record := snapshot.byPath[normalized]
		if record == nil {
			return "", false
		}
		return record.npkRelative, true
	}, directory
}

// witnessSet returns the entries whose content decides this preview: every
// closure node, because a changed payload can add or drop a reference, plus
// every registry list, because a changed row re-resolves identifiers the
// closure was walked with.
func (s *ExtractionService) witnessSet(closure *extraction.Closure) []int32 {
	registers := s.scanners.registryWitness()
	out := make([]int32, 0, len(closure.Nodes)+len(registers))
	for _, node := range closure.Nodes {
		out = append(out, node.Index)
	}
	return append(out, registers...)
}

// currentArchive reads the live archive, the generation installed with it and
// the active annotation engine, which are the three things a derived cache may
// depend on: a rule reload replaces the engine, and the relation table the
// scanner caches comes from the same document.
func (s *ExtractionService) currentArchive() (*pvf.Archive, uint64, *annotations.Engine) {
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()
	return s.c.archive, s.c.archiveGeneration, s.c.annotationEngine
}

// executablePlan returns the cached preview only while it is still valid, so
// Execute and ExecuteDialog refuse a stale id identically and neither can pop a
// dialog for a plan that would fail anyway.
func (s *ExtractionService) executablePlan(id string) *extractionPlan {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	current := s.plan
	s.mu.Unlock()
	if current == nil || current.preview.ID != id || !s.valid(current) {
		return nil
	}
	return current
}

// ExecuteDialog asks for the destination directory and then writes the frozen
// preview. The application already picks export destinations this way; making an
// absolute path a typed text field was the alternative that invited typos and a
// rejected write, so the panel uses the same dialog as 导出. The full result is
// returned rather than just the path because "181 个文件 / 0.55 MiB / 6 个登记表片段"
// is what tells the user the extraction actually happened.
func (s *ExtractionService) ExecuteDialog(id string) (*ExtractionResult, error) {
	if s.executablePlan(id) == nil {
		return nil, ErrExtractionStale
	}
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseFiles(false).CanChooseDirectories(true).CanCreateDirectories(true).
		SetTitle("选择提取目标目录").PromptForSingleSelection()
	if err != nil || dir == "" {
		return nil, err
	}
	return s.Execute(id, dir)
}

// Execute writes the frozen preview to disk. Nothing is recomputed, so an entry
// excluded by the selection cannot reappear. The content witness is the one
// thing re-checked: exporting a set that no longer matches what the user
// approved is worse than exporting nothing.
func (s *ExtractionService) Execute(id, dir string) (*ExtractionResult, error) {
	if dir == "" {
		return nil, fmt.Errorf("请选择导出目录")
	}
	current := s.executablePlan(id)
	if current == nil {
		return nil, ErrExtractionStale
	}
	locator, npkDirectory := s.assetLocator()
	result, err := extraction.Export(current.archive, current.closure, extraction.ExportOptions{
		Dir: dir, WithReports: true, Plan: current.plan,
		LstSuggest: current.request.LstSuggest, Scanner: current.scanner,
		Assets: locator,
	})
	if err != nil {
		return nil, err
	}
	containers := assetContainers(result.Assets)
	copied, copiedBytes := 0, int64(0)
	if current.request.CopyAssets {
		// 承诺了图片就要么给全要么报错：只写脚本却报成功，正是本项目最贵的
		// "残缺读起来像完整"。拷贝失败时 .part 临时文件已清掉，不会留半容器。
		var copyErr error
		copied, copiedBytes, copyErr = copyAssetContainers(npkDirectory, result.Dir, containers)
		if copyErr != nil {
			return nil, copyErr
		}
	}
	return &ExtractionResult{
		Dir: result.Dir, Files: result.Files, Bytes: result.Bytes,
		AutoCompleted: len(current.plan.External), Dangling: len(current.plan.Dangling),
		LstSuggestions:    result.Suggestions,
		RegistryFragments: result.FragmentFiles,
		AssetTotal:        result.Assets.Total,
		AssetMatched:      result.Assets.Matched,
		AssetDynamic:      result.Assets.Dynamic,
		AssetSheet:        result.AssetFile,
		AssetContainers:   containers,
		AssetCopied:       copied,
		AssetCopiedBytes:  copiedBytes,
	}, nil
}

// assetContainers lists the distinct containers behind the matched images in
// stable order. Repeats are collapsed: one NPK holds most of a character's
// frames, and a copy list with duplicates reads like a bug.
func assetContainers(inventory extraction.AssetInventory) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, record := range inventory.Records {
		if record.NPK == "" || seen[record.NPK] {
			continue
		}
		seen[record.NPK] = true
		out = append(out, record.NPK)
	}
	sort.Strings(out)
	return out
}

// projectAssetBytes sums the size of the containers an export would copy. A
// missing or unreadable container counts as zero rather than failing the
// preview: the sheet already reports per-image attribution, and a preview must
// not break because one file moved.
func projectAssetBytes(npkDirectory string, containers []string) int64 {
	if npkDirectory == "" {
		return 0
	}
	var total int64
	for _, container := range containers {
		full := filepath.Join(npkDirectory, filepath.FromSlash(container))
		if !isImagePathWithin(npkDirectory, full) {
			continue
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		total += info.Size()
	}
	return total
}

// copyAssetContainers writes the containers into the package's npk/ folder.
// Nothing here invents a container name: the list comes from the image index, so
// an image the index cannot place simply does not bring a file along, and the
// sheet says so.
func copyAssetContainers(npkDirectory, root string, containers []string) (int, int64, error) {
	if npkDirectory == "" || len(containers) == 0 {
		return 0, 0, nil
	}
	target := filepath.Join(root, "npk")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, 0, err
	}
	copied, bytes := 0, int64(0)
	for _, container := range containers {
		source := filepath.Join(npkDirectory, filepath.FromSlash(container))
		if !isImagePathWithin(npkDirectory, source) {
			return copied, bytes, fmt.Errorf("NPK 容器路径越界: %s", container)
		}
		name := filepath.Base(source)
		if name == "." || name == string(filepath.Separator) || strings.ContainsAny(name, `/\:*?"<>|`) {
			return copied, bytes, fmt.Errorf("NPK 容器名非法: %s", container)
		}
		destination := filepath.Join(target, name)
		// 容器可达数百 MB，流式拷贝并先落 .part 再改名：中途失败不会留下半个包。
		file, err := os.Open(source)
		if err != nil {
			return copied, bytes, fmt.Errorf("读取 NPK 容器 %s 失败: %w", container, err)
		}
		partial := destination + ".part"
		out, err := os.Create(partial)
		if err != nil {
			file.Close()
			return copied, bytes, err
		}
		written, err := io.Copy(out, file)
		closeSource := file.Close()
		closeOut := out.Close()
		if err != nil || closeSource != nil || closeOut != nil {
			os.Remove(partial)
			return copied, bytes, fmt.Errorf("拷贝 NPK 容器 %s 失败: %v", container, err)
		}
		if err := os.Rename(partial, destination); err != nil {
			os.Remove(partial)
			return copied, bytes, err
		}
		copied++
		bytes += written
	}
	return copied, bytes, nil
}

// valid reports whether a cached preview may still be executed. Age, archive
// identity and generation cover reloads and structural edits; the content
// witness is what catches an in-place edit, because that changes none of them.
func (s *ExtractionService) valid(p *extractionPlan) bool {
	if p == nil || time.Since(p.created) >= extractionPlanTTL {
		return false
	}
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()
	if s.c.archive != p.archive || s.c.archiveGeneration != p.generation {
		return false
	}
	return extraction.WatchEntries(s.c.archive, p.watchSet) == p.watch
}

// Release drops a cached preview.
func (s *ExtractionService) Release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan != nil && s.plan.preview.ID == id {
		s.clearPlanLocked()
	}
}

// clearPlanLocked drops the cached preview and its expiry timer.
func (s *ExtractionService) clearPlanLocked() {
	s.plan = nil
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
}

// scannerFor returns a warmed scanner for this archive, rebuilding it only when
// what it caches has changed. The first call pays the one-time warm-up (basename
// index plus registries), which is why it is cached at all.
func (s *ExtractionService) scannerFor(archive *pvf.Archive, generation uint64, rules *annotations.Engine) (*extraction.Scanner, error) {
	// Resolutions may now differ, so a preview computed against the old tables
	// must not stay executable.
	return s.scanners.forArchive(s.c, archive, generation, rules, coreWitness(s.c), s.clearPlanLocked)
}

// validLocked reports whether a cached preview may still be executed. The
// archive pointer and age are the two things a preview depends on.
func (s *ExtractionService) validLocked(p *extractionPlan) bool {
	if p == nil {
		return false
	}
	if time.Since(p.created) >= extractionPlanTTL {
		return false
	}
	return p.archive == s.c.archive
}

func nextPlanID() string {
	return fmt.Sprintf("extract-%d", planSequence.Add(1))
}
