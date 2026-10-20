package services

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"pvfine/internal/appdirs"
	"pvfine/internal/extraction"
)

// extractionCatalogFile is the optional user override for the reference-edge
// catalog. It sits next to the exe like every other piece of persistent data, so
// a green install can carry it along. Absence means "use the embedded catalog":
// the override is an extra, never a requirement.
const extractionCatalogFile = "extract-edges.json"

// maxExternalCatalogBytes bounds a user-editable file. The shipped catalog is
// roughly 150 KB; anything past this is an accident (or not a catalog), and
// reading it would stall every preview for no benefit.
const maxExternalCatalogBytes = 8 << 20

// externalCatalogTemplate is what startup writes when no override exists. It is
// deliberately an empty partial document, not a copy of the builtin catalog: a
// full copy would override every same-id edge at the version it was copied at,
// silently undoing later builtin fixes.
const externalCatalogTemplate = `{
  "version": 1,
  "description": "pvfine 提取中心的外部边目录。只写要覆盖或新增的 categories / edges：同名 id 整条覆盖内置声明，新 id 追加，内置未提到的保留。保存后下一次预览生效。",
  "categories": [],
  "edges": []
}
`

// ExtractionCatalogStatus says which catalog is in effect and why. The panel
// shows it because a silently overridden catalog is indistinguishable from a
// broken one: an edge the user added but the app did not pick up would otherwise
// look like a traversal bug.
type ExtractionCatalogStatus struct {
	// Source is "builtin" or "external".
	Source string `json:"source"`
	// Path is the external file when one exists.
	Path string `json:"path,omitempty"`
	// EdgeCount is the effective catalog's edge count after merging.
	EdgeCount int `json:"edgeCount"`
	// BuiltinEdgeCount is how many edges ship with the application.
	BuiltinEdgeCount int `json:"builtinEdgeCount"`
	// ExternalEdgeCount is how many edges the external file declares.
	ExternalEdgeCount int `json:"externalEdgeCount"`
	// ExternalCategoryCount is how many categories it declares, so an
	// initialized-but-empty file can say "no overrides yet" instead of looking
	// like an active override.
	ExternalCategoryCount int `json:"externalCategoryCount"`
	// Warning carries a load or validation error. The effective catalog stays
	// the last one that parsed, so a half-written file never breaks extraction.
	Warning string `json:"warning,omitempty"`
}

// catalogSnapshot couples the effective catalog with the identity a scanner
// cache must key on. Key changes exactly when the effective catalog changes, so
// a failed reload of an invalid file does not force a pointless scanner rebuild.
type catalogSnapshot struct {
	catalog *extraction.Catalog
	status  ExtractionCatalogStatus
	key     string
}

// catalogSource resolves the effective edge catalog on demand. Every call stats
// the external file, so editing it takes effect on the next preview without a
// restart and without a file watcher.
type catalogSource struct {
	// path overrides the data-directory lookup; tests set it.
	path string

	builtinOnce    sync.Once
	builtin        extraction.Document
	builtinCatalog *extraction.Catalog
	builtinErr     error

	mu sync.Mutex
	// stamp is the last metadata stamp that was attempted, successful or not.
	stamp string
	// key identifies the effective catalog ("builtin" or "external:<stamp>").
	key     string
	catalog *extraction.Catalog
	status  ExtractionCatalogStatus
}

var extractionCatalog = &catalogSource{}

// Load returns the catalog to use for this operation. The error is reserved for
// a broken embedded document, which is a build defect rather than user input.
func (cs *catalogSource) Load() (catalogSnapshot, error) {
	if err := cs.initBuiltin(); err != nil {
		return catalogSnapshot{}, err
	}
	path, pathErr := cs.externalPath()
	// A missing data directory is not an override problem: fall back to the
	// embedded catalog and let the panel say why the file cannot be located.
	if pathErr != nil {
		path = ""
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()
	builtinStatus := ExtractionCatalogStatus{
		Source: "builtin", EdgeCount: cs.builtinCatalog.EdgeCount(),
		BuiltinEdgeCount: len(cs.builtin.Edges),
	}
	if path == "" {
		cs.useBuiltinLocked(builtinStatus)
		if pathErr != nil {
			cs.status.Warning = "无法定位外部边目录：" + pathErr.Error()
		}
		return cs.snapshotLocked(), nil
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			// 重置戳：文件删掉后重建时，即便 mtime+size 与旧文件相同也必须重新解析。
			cs.stamp = ""
			cs.useBuiltinLocked(builtinStatus)
			return cs.snapshotLocked(), nil
		}
		cs.keepLastGoodLocked(builtinStatus, path, "读取外部边目录失败："+statErr.Error())
		return cs.snapshotLocked(), nil
	}
	stamp := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if cs.catalog != nil && cs.stamp == stamp {
		return cs.snapshotLocked(), nil
	}
	cs.stamp = stamp
	if info.Size() > maxExternalCatalogBytes {
		cs.keepLastGoodLocked(builtinStatus, path, fmt.Sprintf(
			"外部边目录超过 %d MiB 上限（%d 字节）", maxExternalCatalogBytes>>20, info.Size()))
		return cs.snapshotLocked(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		cs.keepLastGoodLocked(builtinStatus, path, "读取外部边目录失败："+err.Error())
		return cs.snapshotLocked(), nil
	}
	var external extraction.Document
	if err := json.Unmarshal(data, &external); err != nil {
		cs.keepLastGoodLocked(builtinStatus, path, "解析外部边目录失败："+err.Error())
		return cs.snapshotLocked(), nil
	}
	merged, err := mergeCatalogDocuments(cs.builtin, external)
	if err != nil {
		cs.keepLastGoodLocked(builtinStatus, path, err.Error())
		return cs.snapshotLocked(), nil
	}
	catalog, err := extraction.NewCatalog(merged)
	if err != nil {
		cs.keepLastGoodLocked(builtinStatus, path, err.Error())
		return cs.snapshotLocked(), nil
	}
	cs.catalog = catalog
	cs.key = "external:" + stamp
	cs.status = ExtractionCatalogStatus{
		Source: "external", Path: path, EdgeCount: catalog.EdgeCount(),
		BuiltinEdgeCount: len(cs.builtin.Edges), ExternalEdgeCount: len(external.Edges),
		ExternalCategoryCount: len(external.Categories),
	}
	return cs.snapshotLocked(), nil
}

// EnsureFile creates the empty partial document when no override exists, so the
// file is discoverable without shipping a full copy that would pin every edge
// at its current declaration. An existing file is never touched.
func (cs *catalogSource) EnsureFile() error {
	path, err := cs.externalPath()
	if err != nil {
		return err
	}
	_, err = ensureConfigFile(path, []byte(externalCatalogTemplate))
	return err
}

func (cs *catalogSource) externalPath() (string, error) {
	if cs.path != "" {
		return cs.path, nil
	}
	return appdirs.DataFile(extractionCatalogFile)
}

// Status returns just the description of the effective catalog.
func (cs *catalogSource) Status() (ExtractionCatalogStatus, error) {
	snapshot, err := cs.Load()
	if err != nil {
		return ExtractionCatalogStatus{}, err
	}
	return snapshot.status, nil
}

// Key returns the effective catalog identity for cache validity checks.
func (cs *catalogSource) Key() string {
	snapshot, err := cs.Load()
	if err != nil {
		return ""
	}
	return snapshot.key
}

func (cs *catalogSource) initBuiltin() error {
	cs.builtinOnce.Do(func() {
		if err := json.Unmarshal(extraction.EmbeddedEdges, &cs.builtin); err != nil {
			cs.builtinErr = fmt.Errorf("解析内置边目录失败: %w", err)
			return
		}
		catalog, err := extraction.NewCatalog(cs.builtin)
		if err != nil {
			cs.builtinErr = err
			return
		}
		cs.builtinCatalog = catalog
	})
	return cs.builtinErr
}

func (cs *catalogSource) useBuiltinLocked(status ExtractionCatalogStatus) {
	cs.catalog = cs.builtinCatalog
	cs.key = "builtin"
	cs.status = status
}

// keepLastGoodLocked keeps the catalog that last parsed, so a save in progress
// or a typo cannot break a working session. Before any external file has parsed,
// "last good" is the embedded catalog. The path is part of the warning because
// the effective source may still read "builtin" while the broken file is what
// the user needs to find.
func (cs *catalogSource) keepLastGoodLocked(builtinStatus ExtractionCatalogStatus, path, warning string) {
	if cs.catalog == nil {
		cs.useBuiltinLocked(builtinStatus)
	}
	source := "内置"
	if cs.status.Source == "external" {
		source = "上一次可用的外部"
	}
	cs.status.Path = path
	cs.status.Warning = "外部文件 " + path + "：" + warning + "（继续使用" + source + "目录）"
}

func (cs *catalogSource) snapshotLocked() catalogSnapshot {
	return catalogSnapshot{catalog: cs.catalog, status: cs.status, key: cs.key}
}

// mergeCatalogDocuments overlays the external document onto the built-in one by
// id. External edges and categories replace same-id declarations in place and
// new ones append; built-in declarations the file does not mention stay. That is
// what lets an upgrade ship new edges to users who carry an override.
func mergeCatalogDocuments(builtin, external extraction.Document) (extraction.Document, error) {
	if external.Version != builtin.Version {
		return extraction.Document{}, fmt.Errorf(
			"外部边目录版本 %d 不被支持（当前 %d）", external.Version, builtin.Version)
	}
	merged := extraction.Document{Version: builtin.Version, Description: builtin.Description}
	merged.Categories = append([]extraction.Category(nil), builtin.Categories...)
	categoryIndex := make(map[string]int, len(merged.Categories))
	for i := range merged.Categories {
		categoryIndex[merged.Categories[i].ID] = i
	}
	seenCategory := make(map[string]bool, len(external.Categories))
	for _, category := range external.Categories {
		if category.ID == "" {
			return extraction.Document{}, fmt.Errorf("外部边目录有类别缺少 id")
		}
		if seenCategory[category.ID] {
			return extraction.Document{}, fmt.Errorf("外部边目录的类别 id 重复：%s", category.ID)
		}
		seenCategory[category.ID] = true
		if i, ok := categoryIndex[category.ID]; ok {
			merged.Categories[i] = category
			continue
		}
		categoryIndex[category.ID] = len(merged.Categories)
		merged.Categories = append(merged.Categories, category)
	}

	merged.Edges = append([]extraction.Edge(nil), builtin.Edges...)
	edgeIndex := make(map[string]int, len(merged.Edges))
	for i := range merged.Edges {
		edgeIndex[strings.ToLower(merged.Edges[i].ID)] = i
	}
	seenEdge := make(map[string]bool, len(external.Edges))
	for i, edge := range external.Edges {
		id := strings.ToLower(strings.TrimSpace(edge.ID))
		if id == "" {
			return extraction.Document{}, fmt.Errorf("外部边目录第 %d 条边缺少 id", i+1)
		}
		if seenEdge[id] {
			return extraction.Document{}, fmt.Errorf("外部边目录的边 id 重复：%s", edge.ID)
		}
		seenEdge[id] = true
		if at, ok := edgeIndex[id]; ok {
			merged.Edges[at] = edge
			continue
		}
		edgeIndex[id] = len(merged.Edges)
		merged.Edges = append(merged.Edges, edge)
	}
	return merged, nil
}
