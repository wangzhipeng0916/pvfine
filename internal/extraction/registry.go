package extraction

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

type registry struct {
	name     string
	listPath string
	dir      string
	// byID maps an identifier to its registered relative path, unmodified. It is
	// written only while the registry loads and read-only afterwards.
	byID map[string]string
	// resolved caches identifier -> archive index, with -1 for known misses.
	// Concurrent scans share one RegistrySet, so the cache must be safe for
	// parallel use while byID stays a plain map.
	resolved sync.Map
	rows     int
	elapsed  time.Duration
}

// RegistrySet lazily resolves identifiers through the relation declarations in
// config/lists.json. Registries are parsed on first use so that an on-demand
// closure pays only for the lists it actually touches.
type RegistrySet struct {
	archive *pvf.Archive
	specs   map[string]annotations.RelationSpec
	// mu guards the lazy load state; Resolve itself only takes it on a miss.
	mu       sync.Mutex
	loaded   map[string]*registry
	failures map[string]error
	loads    int
	elapsed  time.Duration
	// Fallbacks records how often non-primary path candidates matched, which
	// is the signal that a registry deviates from the list-relative convention.
	Fallbacks int64
}

// NewRegistrySet binds relation specifications to an open archive.
func NewRegistrySet(a *pvf.Archive, specs map[string]annotations.RelationSpec) *RegistrySet {
	return &RegistrySet{archive: a, specs: specs,
		loaded: make(map[string]*registry), failures: make(map[string]error)}
}

// PreloadAll materializes every declared relation so parallel scans never race
// on lazy loading. Cancellation is honoured between relations.
func (rs *RegistrySet) PreloadAll(ctx context.Context) int {
	if rs == nil {
		return 0
	}
	names := make([]string, 0, len(rs.specs))
	for name := range rs.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	loaded := 0
	for _, name := range names {
		if ctx != nil && ctx.Err() != nil {
			return loaded
		}
		if _, err := rs.registry(name); err == nil {
			loaded++
		}
	}
	return loaded
}

// Loads reports how many registries have been parsed.
func (rs *RegistrySet) Loads() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.loads
}

// LoadTime reports total time spent parsing registries.
func (rs *RegistrySet) LoadTime() time.Duration {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.elapsed
}

// Rows reports the number of registered rows loaded for one relation, or -1 if
// the relation is unknown.
func (rs *RegistrySet) Rows(relation string) int {
	r, err := rs.registry(relation)
	if err != nil {
		return -1
	}
	return r.rows
}

// ResolvedTarget is a registry resolution outcome. Index is -1 when the row is
// missing or its file is absent; Reason then explains which of the two it was.
type ResolvedTarget struct {
	Index  int32
	Path   string
	Reason string
}

// Resolve maps an identifier of the named relation to an archive entry.
func (rs *RegistrySet) Resolve(relation, id string) (ResolvedTarget, error) {
	r, err := rs.registry(relation)
	if err != nil {
		return ResolvedTarget{Index: -1}, err
	}
	relative, ok := r.byID[id]
	if !ok {
		return ResolvedTarget{Index: -1, Path: r.listPath + ":" + id, Reason: "registry 无此 ID"}, nil
	}
	want := path.Join(r.dir, relative)
	if cached, seen := r.resolved.Load(id); seen {
		if index := cached.(int32); index >= 0 {
			return ResolvedTarget{Index: index, Path: rs.archive.FullPath(index)}, nil
		}
		return ResolvedTarget{Index: -1, Path: want, Reason: "注册目标条目不存在"}, nil
	}
	index, canonical, err := rs.locate(r.dir, relative)
	if err != nil {
		r.resolved.Store(id, int32(-1))
		return ResolvedTarget{Index: -1, Path: want, Reason: "注册目标条目不存在"}, nil
	}
	r.resolved.Store(id, index)
	if !strings.EqualFold(canonical, want) {
		atomic.AddInt64(&rs.Fallbacks, 1)
	}
	return ResolvedTarget{Index: index, Path: canonical}, nil
}

// ListFiles returns every archive path the set may read: a declared list, each
// contextual alternative, and the concrete lists behind a union. A caller that
// has to notice registry edits uses this to pick the entries to watch, because
// a changed .lst row changes resolution without changing any entry count.
func (rs *RegistrySet) ListFiles() []string {
	seenRelation := make(map[string]bool, len(rs.specs))
	seenPath := make(map[string]bool, len(rs.specs))
	out := []string{}
	add := func(p string) {
		if p == "" || strings.HasPrefix(p, "union(") {
			return
		}
		key := normalizeRegistryPath(p)
		if seenPath[key] {
			return
		}
		seenPath[key] = true
		out = append(out, p)
	}
	var walk func(name string)
	walk = func(name string) {
		if seenRelation[name] {
			return
		}
		seenRelation[name] = true
		spec, ok := rs.specs[name]
		if !ok {
			return
		}
		if strings.EqualFold(spec.Kind, "union") {
			for _, member := range spec.Relations {
				walk(member)
			}
			return
		}
		names := make([]string, 0, len(spec.ContextPaths))
		for _, p := range spec.ContextPaths {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			add(p)
		}
		add(spec.ListPath)
	}
	names := make([]string, 0, len(rs.specs))
	for name := range rs.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		walk(name)
	}
	sort.Strings(out)
	return out
}

// Relations lists the relation names this set knows.
func (rs *RegistrySet) Relations() []string {
	out := make([]string, 0, len(rs.specs))
	for name := range rs.specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PathIndex maps a normalized registered path to its identifier, for asking
// "is this file registered, and under which id?". The key must be the path the
// archive actually has: rows are written relative to their own list directory on
// 86/90 (`act1/lorien.dgn`) but already carry the directory on paged110
// (`dungeon/act1/lorien.dgn`), so blind joining `dir + row` produced
// `dungeon/dungeon/act1/...` for every row of a 110 table — and this map feeds
// 反向可达性/孤儿检测与登记建议，错起来是整片错。
func (rs *RegistrySet) PathIndex(relation string) (map[string]string, error) {
	r, err := rs.registry(relation)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(r.byID))
	for id, relative := range r.byID {
		key := normalizeRegistryPath(path.Join(r.dir, relative))
		if _, canonical, err := rs.locate(r.dir, relative); err == nil {
			key = normalizeRegistryPath(canonical)
		}
		out[key] = id
	}
	return out, nil
}

// ListPath reports the registry's list file, so a caller can point a reviewer
// at the exact file a suggestion would touch. Union relations report the list
// they were declared with; use LeafRegistries to reach the concrete tables.
func (rs *RegistrySet) ListPath(relation string) (string, error) {
	r, err := rs.registry(relation)
	if err != nil {
		return "", err
	}
	return r.listPath, nil
}

// LeafRegistry is one concrete .lst with the rows it holds, which is what a
// registration fragment has to be written against: a union such as 物品 spans
// two real tables, and a row only makes sense in the one that owns it.
type LeafRegistry struct {
	Relation string
	ListPath string
	// Rows maps an id to the path exactly as the .lst spells it.
	Rows map[string]string
}

// LeafRegistries expands a relation into its concrete lists, following union
// and contextual declarations the same way loading does.
func (rs *RegistrySet) LeafRegistries(relation string) []LeafRegistry {
	spec, ok := rs.specs[relation]
	if !ok {
		return nil
	}
	if strings.EqualFold(spec.Kind, "union") {
		out := []LeafRegistry{}
		for _, member := range spec.Relations {
			out = append(out, rs.LeafRegistries(member)...)
		}
		return out
	}
	listPath := spec.ListPath
	if strings.EqualFold(spec.Kind, "contextual") {
		names := make([]string, 0, len(spec.ContextPaths))
		for _, p := range spec.ContextPaths {
			names = append(names, p)
		}
		if len(names) == 0 {
			return nil
		}
		sort.Strings(names)
		listPath = names[0]
	}
	r, err := rs.registry(relation)
	if err != nil {
		return nil
	}
	return []LeafRegistry{{Relation: relation, ListPath: listPath, Rows: r.byID}}
}

// RelationExtensions lists the extensions the relation's current rows use.
// It is how a suggestion decides whether a file even belongs to a registry:
// the rows themselves are the evidence, not a hard-coded table.
func (rs *RegistrySet) RelationExtensions(relation string) ([]string, error) {
	index, err := rs.PathIndex(relation)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for registered := range index {
		ext := strings.ToLower(path.Ext(registered))
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		out = append(out, ext)
	}
	sort.Strings(out)
	return out, nil
}

// normalizeRegistryPath makes a path comparable regardless of case and of the
// separator style the .lst happened to use.
func normalizeRegistryPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(path.Clean(p), "\\", "/"))
}

// registry materializes one relation, following union members. Loading happens
// outside the lock so union members can recurse; two goroutines racing on the
// same relation may both parse it, which costs only duplicate reads and never
// exposes a partially built table.
func (rs *RegistrySet) registry(relation string) (*registry, error) {
	rs.mu.Lock()
	r, done := rs.loaded[relation]
	failure := rs.failures[relation]
	rs.mu.Unlock()
	if done {
		if r == nil {
			// 建表失败过一次就不要再谎报"relation 没声明"：真实原因（登记表文件
			// 在这份归档里不存在，例如 110 版把表放在 list/ 下）才是用户需要看到的。
			if failure != nil {
				return nil, failure
			}
			return nil, newUnknownRelation(relation)
		}
		return r, nil
	}
	built, err := rs.build(relation)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if existing, ok := rs.loaded[relation]; ok {
		if existing == nil {
			if err := rs.failures[relation]; err != nil {
				return nil, err
			}
			return nil, newUnknownRelation(relation)
		}
		return existing, nil
	}
	if err != nil {
		rs.loaded[relation] = nil
		rs.failures[relation] = err
		return nil, err
	}
	rs.loaded[relation] = built
	return built, nil
}

// build materializes one relation without holding the set lock.
func (rs *RegistrySet) build(relation string) (*registry, error) {
	spec, ok := rs.specs[relation]
	if !ok {
		return nil, newUnknownRelation(relation)
	}
	if strings.EqualFold(spec.Kind, "union") {
		merged := &registry{name: relation, listPath: "union(" + strings.Join(spec.Relations, ",") + ")",
			byID: map[string]string{}}
		for _, member := range spec.Relations {
			sub, err := rs.registry(member)
			if err != nil {
				continue
			}
			for k, v := range sub.byID {
				if _, exists := merged.byID[k]; exists {
					continue
				}
				// 成员行的相对路径以各自 .lst 所在目录为基准。并表时必须先拼上该
				// 目录，否则合并后的 dir 为空，所有 union 查找都会退化到归档根而
				// 全数落空（实测 物品 的 162,567 行没有一行能命中）。
				//
				// 但拼接只对 86/90 的"行写相对路径"成立：paged110 的行本身就带目录，
				// 拼上去得到 `stackable/stackable/gold.stk`，而并表 Resolve 不再走
				// locate，于是整张 物品 表全灭（实测 110 的 mob.dropItem 1,196 值
				// 0 解析）。所以按 locate 的真实落点登记，找不到才退回拼法。
				registered := path.Join(sub.dir, v)
				if _, canonical, err := rs.locate(sub.dir, v); err == nil {
					registered = canonical
				}
				merged.byID[k] = registered
				merged.rows++
			}
		}
		return merged, nil
	}
	if strings.EqualFold(spec.Kind, "contextual") {
		// Contextual relations need a caller-supplied context; without one the
		// first declared list is used, which keeps union-like lookups working.
		names := make([]string, 0, len(spec.ContextPaths))
		for _, p := range spec.ContextPaths {
			names = append(names, p)
		}
		if len(names) == 0 {
			return nil, newUnknownRelation(relation)
		}
		sort.Strings(names)
		return rs.loadFrom(relation, names[0], spec)
	}
	return rs.loadFrom(relation, spec.ListPath, spec)
}

func (rs *RegistrySet) loadFrom(relation, listPath string, spec annotations.RelationSpec) (*registry, error) {
	started := time.Now()
	r := &registry{name: relation, listPath: listPath, dir: path.Dir(listPath),
		byID: map[string]string{}}
	// 登记表位置随版本不同：86/90 把表放在各自内容目录（`dungeon/dungeon.lst`），
	// paged110 集中在 `list/`（`list/dungeon.lst`）。FindList 就是为这件事存在的
	// （先按声明路径找，再试 list/<文件名>），用它才不会让 110 版整个 registry 侧
	// 静默全灭——实测同一份边目录在 110 上 registry 边解析率 0.000、missing 320 万。
	index, ok := rs.archive.FindList(listPath)
	if !ok {
		return nil, fmt.Errorf("extraction: relation %q 的登记表 %s 在这份归档里不存在", relation, listPath)
	}
	text, err := rs.archive.Text(index)
	if err != nil {
		return nil, err
	}
	record := spec.RecordTokens
	if record < 2 {
		record = 2
	}
	idAt, pathAt := spec.IDToken, spec.PathToken
	if idAt < 0 || idAt >= record {
		idAt = 0
	}
	if pathAt < 0 || pathAt >= record {
		pathAt = 1
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < record {
			continue
		}
		for base := 0; base+record <= len(fields); base += record {
			id := strings.TrimSpace(fields[base+idAt])
			p := strings.ReplaceAll(strings.TrimSpace(fields[base+pathAt]), "`", "")
			if id == "" || p == "" {
				continue
			}
			// A registry row must look like a path; a header or a stray
			// numeric table row would otherwise poison the map.
			if !strings.ContainsAny(p, "./\\") && path.Ext(p) == "" {
				continue
			}
			if _, exists := r.byID[id]; !exists {
				r.byID[id] = p
				r.rows++
			}
		}
	}
	r.elapsed = time.Since(started)
	rs.mu.Lock()
	rs.loads++
	rs.elapsed += r.elapsed
	rs.mu.Unlock()
	return r, nil
}

// locate tries, in order: list-directory relative, the `(r)` localized
// sibling, then the archive root. Registry rows are relative to their own
// list directory and mix letter case, while the archive index is matched case
// insensitively.
func (rs *RegistrySet) locate(dir, relative string) (int32, string, error) {
	relative = strings.ReplaceAll(strings.TrimSpace(relative), "\\", "/")
	primary := path.Join(dir, relative)
	candidates := []string{primary, path.Join(path.Dir(primary), "(r)"+path.Base(primary))}
	if root := strings.Trim(relative, "/"); root != "" && !strings.Contains(root, "..") {
		candidates = append(candidates, path.Clean(root))
	}
	for _, candidate := range candidates {
		if index, ok := rs.archive.Find(candidate); ok {
			return index, rs.archive.FullPath(index), nil
		}
	}
	return -1, "", errNotFound
}

type unknownRelationError struct{ relation string }

func (e unknownRelationError) Error() string {
	return "extraction: 边目录引用了未声明的 relation: " + e.relation
}

func newUnknownRelation(relation string) error { return unknownRelationError{relation: relation} }

// ErrNotFound reports that no path candidate matched an archive entry.
var ErrNotFound = errors.New("extraction: 目标条目不在归档中")

var errNotFound = ErrNotFound

// IsUnknownRelation reports whether err came from an undeclared relation name.
func IsUnknownRelation(err error) bool {
	_, ok := err.(unknownRelationError)
	return ok
}
