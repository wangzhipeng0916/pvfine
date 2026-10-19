package extraction

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Record describes how the flat token stream of one section splits into
// records. PVF stores a section's values as a single tab-separated stream, so
// the column position of a reference is part of the format's semantics and
// cannot be inferred from the section name alone.
type Record struct {
	// Width is the number of value fields per record. Zero means every field
	// is a reference.
	Width int `json:"width,omitempty"`
	// RefOffsets lists which field positions hold references. Ignored when
	// Width is zero.
	RefOffsets []int `json:"refOffsets,omitempty"`
	// CountPrefixed declares a record whose width is stated by the data itself
	// instead of being constant, such as a map's [special passive object].
	CountPrefixed *CountPrefixed `json:"countPrefixed,omitempty"`
}

// CountPrefixed describes a record of the shape
//
//	<prefix…> n <item>×n
//
// where n is an integer token inside the record and every item is a kind tag
// followed by a payload. Measured on 86 for .map [special passive object]:
// `ID x y z n kind payload…` renders as `222 1170 204 0 1 [trap] 2011 1 1 3 -1`,
// so PrefixTokens=5, CountIndex=4 and ItemTokens=6. Parsing needs no source
// lines: the count column plus the item size make the record self-synchronising.
type CountPrefixed struct {
	// PrefixTokens is the record length before the items, including the count.
	PrefixTokens int `json:"prefixTokens"`
	// CountIndex is the record-relative index of the item count.
	CountIndex int `json:"countIndex"`
	// ItemTokens is the item length for the ordinary (leaf) kinds.
	ItemTokens int `json:"itemTokens"`
	// KindIndex is the item-relative index of the kind tag; 0 when omitted.
	KindIndex int `json:"kindIndex,omitempty"`
	// NestedKinds are kind tags whose item body is a paired block
	// `[kind] … [/kind]` instead of a fixed ItemTokens payload.
	NestedKinds []string `json:"nestedKinds,omitempty"`
	// PayloadRefs declares item payload slots that hold references. The
	// knowledge base states that for [special passive object] the meaning of a
	// column depends on the object (kind) type, so they are declared per kind
	// and per slot — never as a blanket "these numbers are ids".
	PayloadRefs []PayloadRef `json:"payloadRefs,omitempty"`
	// PayloadNote documents how the declared slots were established.
	PayloadNote string `json:"payloadNote,omitempty"`
	// NestedRefs declares paired blocks whose body is a sequence of fixed-width
	// records, and which column of those records is an identifier. Without such
	// a declaration the block's numbers stay candidates, which is how a whole
	// dependency class (a hell map's monster groups) goes missing from a closure.
	NestedRefs []NestedRef `json:"nestedRefs,omitempty"`
	// NestedNote documents how the nested declarations were established.
	NestedNote string `json:"nestedNote,omitempty"`
}

// PayloadRef declares one item payload slot as a reference into a registry.
type PayloadRef struct {
	// Kind is the kind tag the slot belongs to, e.g. "trap".
	Kind string `json:"kind"`
	// Slot is the item-relative offset of the reference.
	Slot int `json:"slot"`
	// Relation names the registry the value must resolve in.
	Relation string `json:"relation"`
}

// NestedRef reads one paired block `[kind … /kind]` as records of Width values
// and declares which column carries an identifier. A `[hellparty]` block in 86
// is `group_id weight wave` repeated, so Width=3 and Offset=0; weight and wave
// are deliberately *not* reinterpreted as references.
type NestedRef struct {
	// Kind is the nested block's tag, matched case-insensitively.
	Kind string `json:"kind"`
	// Width is how many values make up one record inside the block.
	Width int `json:"width"`
	// Offset is the record-relative column holding the identifier.
	Offset int `json:"offset"`
	// Index says where those identifiers are resolved.
	Index SectionIndex `json:"sectionIndex"`
}

// SectionIndex resolves a value that addresses a record inside one archive
// entry. The entry is a constant path because the reference itself carries no
// path: a `[hellparty]` group_id says nothing about which file defines groups.
type SectionIndex struct {
	// Path is the archive entry holding the index, e.g. etc/hellparty.etc.
	Path string `json:"path"`
	// IndexSection names the section whose integer values are the keys, in the
	// order the member bodies follow them.
	IndexSection string `json:"indexSection"`
	// MemberSection names the section holding one group's member stream.
	MemberSection string `json:"memberSection"`
	// MemberWidth is how many values describe one member.
	MemberWidth int `json:"memberWidth"`
	// KindOffset is the member-relative column holding the routing kind.
	KindOffset int `json:"kindOffset"`
	// Routes map a member kind onto a registry. Kinds with no route stay
	// unknown instead of falling back to a guess, which is what the knowledge
	// base demands for group members of an unlisted kind.
	Routes []MemberRoute `json:"routes"`
}

// MemberRoute sends members of one kind tag to one registry relation.
type MemberRoute struct {
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
}

// relationFor returns the registry a member kind routes to.
func (si SectionIndex) relationFor(kind string) (string, bool) {
	for _, route := range si.Routes {
		if route.Kind == strings.TrimSpace(kind) {
			return route.Relation, true
		}
	}
	return "", false
}

// nestedRefFor returns the declaration for one nested block kind, or nil.
func (cp *CountPrefixed) nestedRefFor(kind string) *NestedRef {
	for i := range cp.NestedRefs {
		if strings.EqualFold(cp.NestedRefs[i].Kind, kind) {
			return &cp.NestedRefs[i]
		}
	}
	return nil
}

// SectionTable describes a key→value table stored as repeated blocks in one
// entry's section. Offsets count value tokens inside a block, so for
// `[equipment part set]` the key is 0 (the set number) and the value is 1 (its
// `.equ` path) — the name token in between is a display string, and the part
// rows below are deliberately not interpreted.
type SectionTable struct {
	// Path is the archive entry holding the table.
	Path string `json:"path"`
	// Section is the block's section name.
	Section     string `json:"section"`
	KeyOffset   int    `json:"keyOffset"`
	ValueOffset int    `json:"valueOffset"`
}

// Target says what a matched token resolves to.
type Target struct {
	// Kind is entry, registry or asset.
	Kind TargetKind `json:"kind"`
	// Relation names a registry declared in config/lists.json. Required for
	// kind=registry.
	Relation string `json:"relation,omitempty"`
	// NoneValues are identifier texts treated as "no reference". PVF uses 0
	// and -1 as null ids throughout.
	NoneValues []string `json:"noneValues,omitempty"`
	// ExpectExt optionally restricts resolved entry paths.
	ExpectExt []string `json:"expectExt,omitempty"`
	// Roots are extra bases a value may be relative to, tried only after the
	// owner-relative and archive-root attempts have failed. PVF writes some
	// references relative to the *target class* root rather than the referrer's:
	// `.twn [area]` lists `Alfhlyra/Alfhlyra.map` for the entry at
	// `map/alfhlyra/alfhlyra.map`, and `.etc [equipment part set]` lists
	// `character/partset/upperset.equ` for `equipment/character/partset/upperset.equ`.
	// A root beginning with `../` is instead relative to the referrer, which is how
	// `.key [particle filename]` works: the value `key/dummy.ptl` lives next to the
	// `.aic` that imports the key file, not inside the key file's own directory.
	// Measured resolution was 0/217, 0/72 and 0/773 without these rungs, which is
	// why they are declared per edge instead of guessed from the value.
	Roots []string `json:"roots,omitempty"`
	// Table is required when Kind is section-table: the value addresses a block
	// in another entry rather than a path or a .lst row. The referenced entry is
	// recorded but never walked — expanding it would pull every row of the table
	// into a package that needs exactly one of them.
	Table *SectionTable `json:"table,omitempty"`
}

// Edge declares one reference position inside one class of script entries.
type Edge struct {
	ID         string   `json:"id"`
	Extensions []string `json:"extensions"`
	// Section is the innermost section name, matched case-insensitively
	// because PVF mixes `[BASE ANI]` and `[passive object]` capitalizations.
	Section string `json:"section"`
	// Under lists ancestor section names that must appear in the element's
	// SectionPath, which disambiguates reused leaf names such as [INDEX].
	Under []string `json:"under,omitempty"`
	// TokenTypes restricts the match to specific ScriptView token types
	// (0=integer, 2=float, 6=backticked string). Empty means any.
	TokenTypes []int32 `json:"tokenTypes,omitempty"`
	Record     *Record `json:"record,omitempty"`
	Target     Target  `json:"target"`
	// Category optionally overrides which export category the referenced
	// entries belong to. Without it the category is derived from the relation
	// and then from the target's extension.
	Category string `json:"category,omitempty"`
	// Versions optionally scopes the edge to specific archive revisions.
	Versions []string `json:"versions,omitempty"`
	// Note documents provenance for humans reviewing the catalog.
	Note string `json:"note,omitempty"`
}

// Category is one export category, declared so the classification stays data
// rather than code: a new PVF revision adds entries here, never a switch.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Order controls display order; ties fall back to the id.
	Order int `json:"order,omitempty"`
	// Extensions lists the target extensions that belong to this category.
	Extensions []string `json:"extensions,omitempty"`
	// Relations lists registry relations whose references land here.
	Relations []string `json:"relations,omitempty"`
}

// Document is the serialized form of a Catalog.
type Document struct {
	Version     int        `json:"version"`
	Description string     `json:"description,omitempty"`
	Categories  []Category `json:"categories,omitempty"`
	Edges       []Edge     `json:"edges"`
}

// OtherCategory is the bucket for entries no declaration claims. A non-empty
// bucket is a signal that the classification table needs an entry.
const OtherCategory = "other"

// Catalog is a validated, indexed edge set.
type Catalog struct {
	document Document
	// byExt indexes edges per extension and lowercased section name.
	byExt map[string]map[string][]*Edge
	// sections indexes declared section names per extension, used to decide
	// whether a reference-shaped token is unmodeled.
	sections map[string]map[string]bool
	// byID indexes edges by id, so a closure node can recover the edge that
	// pulled it in.
	byID map[string]*Edge
	// categories holds the ordered category declarations plus their lookup
	// tables by extension and by relation.
	categories []Category
	byCategory map[string]Category
	catByExt   map[string]string
	catByRel   map[string]string
	version    int
}

// DefaultNoneValues are the identifier texts PVF uses for "absent".
var DefaultNoneValues = []string{"0", "-1"}

// LoadCatalog parses and validates a catalog document.
func LoadCatalog(data []byte) (*Catalog, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("extraction: 解析边目录失败: %w", err)
	}
	return NewCatalog(doc)
}

// NewCatalog validates and indexes the given document.
func NewCatalog(doc Document) (*Catalog, error) {
	c := &Catalog{
		document:   doc,
		byExt:      make(map[string]map[string][]*Edge),
		sections:   make(map[string]map[string]bool),
		byID:       make(map[string]*Edge),
		byCategory: make(map[string]Category),
		catByExt:   make(map[string]string),
		catByRel:   make(map[string]string),
		version:    doc.Version,
	}
	if len(doc.Edges) == 0 {
		return nil, fmt.Errorf("extraction: 边目录为空")
	}
	if err := c.indexCategories(doc.Categories); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(doc.Edges))
	for i := range doc.Edges {
		e := doc.Edges[i]
		if e.ID == "" {
			return nil, fmt.Errorf("extraction: 第 %d 条边缺少 id", i)
		}
		if seen[strings.ToLower(e.ID)] {
			return nil, fmt.Errorf("extraction: 边 id 重复: %s", e.ID)
		}
		seen[strings.ToLower(e.ID)] = true
		if e.Section == "" {
			return nil, fmt.Errorf("extraction: 边 %s 缺少 section", e.ID)
		}
		if len(e.Extensions) == 0 {
			return nil, fmt.Errorf("extraction: 边 %s 缺少 extensions", e.ID)
		}
		if e.Category != "" {
			if _, ok := c.byCategory[e.Category]; !ok {
				return nil, fmt.Errorf("extraction: 边 %s 引用了未声明的类别 %q", e.ID, e.Category)
			}
		}
		switch e.Target.Kind {
		case TargetRegistry:
			if e.Target.Relation == "" {
				return nil, fmt.Errorf("extraction: 边 %s 是 registry 目标但缺少 relation", e.ID)
			}
		case TargetSectionTable:
			table := e.Target.Table
			if table == nil {
				return nil, fmt.Errorf("extraction: 边 %s 是 section-table 目标但缺少 table", e.ID)
			}
			if strings.TrimSpace(table.Path) == "" || strings.TrimSpace(table.Section) == "" {
				return nil, fmt.Errorf("extraction: 边 %s 的 table 缺少 path 或 section", e.ID)
			}
			if strings.Contains(table.Path, "..") || strings.Contains(table.Path, ":") {
				return nil, fmt.Errorf("extraction: 边 %s 的 table 路径非法: %s", e.ID, table.Path)
			}
			if table.KeyOffset < 0 || table.ValueOffset <= table.KeyOffset {
				return nil, fmt.Errorf("extraction: 边 %s 的 table 需要 0 <= keyOffset < valueOffset", e.ID)
			}
			// 表里取出的仍是路径，所以它和 entry 目标一样需要能声明基准目录。
			fallthrough
		case TargetEntry, TargetAsset:
			if len(e.Target.Roots) > 0 && e.Target.Kind != TargetEntry && e.Target.Kind != TargetSectionTable {
				return nil, fmt.Errorf("extraction: 边 %s 只有 entry 或 section-table 目标能声明 roots", e.ID)
			}
			for _, root := range e.Target.Roots {
				trimmed := strings.ReplaceAll(strings.TrimSpace(root), "\\", "/")
				// 允许两种基准：归档内相对目录（`equipment`），以及相对引用方往上若干层
				// （`../`、`../../`，实测 `.key [particle filename]` 需要这一形态）。
				// 判 ".." 必须在补 "/" 之前：path.Clean("/"+"../etc") 得到 "/etc"，
				// 先补根再把就看不出这是往上跑的声明了。前导段之外一律拒绝。
				lead := 0
				for strings.HasPrefix(trimmed[lead:], "../") {
					lead += 3
				}
				rest := strings.Trim(strings.TrimSpace(trimmed[lead:]), "/")
				if rest == "" && lead == 0 {
					return nil, fmt.Errorf("extraction: 边 %s 的 roots 目录 %q 为空", e.ID, root)
				}
				if strings.Contains(rest, "..") || strings.Contains(rest, ":") {
					return nil, fmt.Errorf("extraction: 边 %s 的 roots 目录 %q 不是归档内的相对目录", e.ID, root)
				}
			}
		default:
			return nil, fmt.Errorf("extraction: 边 %s 的 target.kind 未知: %q", e.ID, e.Target.Kind)
		}
		if cp := e.Record.countPrefixed(); cp != nil {
			if cp.PrefixTokens < 1 {
				return nil, fmt.Errorf("extraction: 边 %s 的 countPrefixed.prefixTokens 必须为正", e.ID)
			}
			if cp.CountIndex < 0 || cp.CountIndex >= cp.PrefixTokens {
				return nil, fmt.Errorf("extraction: 边 %s 的 countPrefixed.countIndex=%d 必须落在前缀 %d 列内",
					e.ID, cp.CountIndex, cp.PrefixTokens)
			}
			if cp.ItemTokens < 1 {
				return nil, fmt.Errorf("extraction: 边 %s 的 countPrefixed.itemTokens 必须为正", e.ID)
			}
			if cp.KindIndex < 0 || cp.KindIndex >= cp.ItemTokens {
				return nil, fmt.Errorf("extraction: 边 %s 的 countPrefixed.kindIndex=%d 必须落在条目 %d 列内",
					e.ID, cp.KindIndex, cp.ItemTokens)
			}
			for _, ref := range cp.PayloadRefs {
				if strings.TrimSpace(ref.Kind) == "" {
					return nil, fmt.Errorf("extraction: 边 %s 的 payloadRefs 缺少 kind", e.ID)
				}
				if ref.Relation == "" {
					return nil, fmt.Errorf("extraction: 边 %s 的 payloadRefs[%s] 缺少 relation", e.ID, ref.Kind)
				}
				if ref.Slot <= cp.KindIndex || ref.Slot >= cp.ItemTokens {
					return nil, fmt.Errorf("extraction: 边 %s 的 payloadRefs[%s].slot=%d 必须落在载荷范围内 (%d..%d)",
						e.ID, ref.Kind, ref.Slot, cp.KindIndex+1, cp.ItemTokens-1)
				}
			}
			for _, ref := range cp.NestedRefs {
				if strings.TrimSpace(ref.Kind) == "" {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs 缺少 kind", e.ID)
				}
				if !containsFold(cp.NestedKinds, ref.Kind) {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s] 未同时登记在 nestedKinds 里，配对块不会被条目遍历解释",
						e.ID, ref.Kind)
				}
				if ref.Width < 1 {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].width 必须为正", e.ID, ref.Kind)
				}
				if ref.Offset < 0 || ref.Offset >= ref.Width {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].offset=%d 必须落在 %d 列记录内",
						e.ID, ref.Kind, ref.Offset, ref.Width)
				}
				idx := ref.Index
				if idx.Path == "" || idx.IndexSection == "" || idx.MemberSection == "" {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].sectionIndex 缺少 path/indexSection/memberSection",
						e.ID, ref.Kind)
				}
				if idx.MemberWidth < 2 {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].memberWidth 至少 2（成员值 + kind）",
						e.ID, ref.Kind)
				}
				if idx.KindOffset < 0 || idx.KindOffset >= idx.MemberWidth {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].kindOffset=%d 必须落在成员 %d 列内",
						e.ID, ref.Kind, idx.KindOffset, idx.MemberWidth)
				}
				if len(idx.Routes) == 0 {
					return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s].routes 为空，成员无处可解析",
						e.ID, ref.Kind)
				}
				for _, route := range idx.Routes {
					if strings.TrimSpace(route.Kind) == "" || route.Relation == "" {
						return nil, fmt.Errorf("extraction: 边 %s 的 nestedRefs[%s] 有一条 route 缺少 kind 或 relation",
							e.ID, ref.Kind)
					}
				}
			}
		}
		edge := e
		c.byID[strings.ToLower(edge.ID)] = &edge
		for _, ext := range edge.Extensions {
			ext = normalizeExt(ext)
			if ext == "" {
				return nil, fmt.Errorf("extraction: 边 %s 的扩展名非法", edge.ID)
			}
			sec := strings.ToLower(edge.Section)
			if c.byExt[ext] == nil {
				c.byExt[ext] = make(map[string][]*Edge)
				c.sections[ext] = make(map[string]bool)
			}
			c.byExt[ext][sec] = append(c.byExt[ext][sec], &edge)
			c.sections[ext][sec] = true
		}
	}
	return c, nil
}

// EdgesIn returns the edges declared for an extension and section pair.
func (c *Catalog) EdgesIn(ext, section string) []*Edge {
	return c.byExt[normalizeExt(ext)][strings.ToLower(section)]
}

// indexCategories validates and indexes the category declarations.
func (c *Catalog) indexCategories(list []Category) error {
	seen := map[string]bool{}
	for i := range list {
		cat := list[i]
		if cat.ID == "" {
			return fmt.Errorf("extraction: 第 %d 个类别缺少 id", i)
		}
		if seen[cat.ID] {
			return fmt.Errorf("extraction: 类别 id 重复: %s", cat.ID)
		}
		seen[cat.ID] = true
		if cat.ID == OtherCategory {
			return fmt.Errorf("extraction: 类别 id 不能是保留值 %q", OtherCategory)
		}
		if cat.Name == "" {
			cat.Name = cat.ID
		}
		for _, ext := range cat.Extensions {
			normalized := normalizeExt(ext)
			if normalized == "" {
				return fmt.Errorf("extraction: 类别 %s 的扩展名非法: %q", cat.ID, ext)
			}
			if owner, exists := c.catByExt[normalized]; exists && owner != cat.ID {
				return fmt.Errorf("extraction: 扩展名 %s 同时属于类别 %s 与 %s", normalized, owner, cat.ID)
			}
			c.catByExt[normalized] = cat.ID
		}
		for _, rel := range cat.Relations {
			rel = strings.TrimSpace(rel)
			if rel == "" {
				continue
			}
			if owner, exists := c.catByRel[rel]; exists && owner != cat.ID {
				return fmt.Errorf("extraction: relation %s 同时属于类别 %s 与 %s", rel, owner, cat.ID)
			}
			c.catByRel[rel] = cat.ID
		}
		c.byCategory[cat.ID] = cat
	}
	c.categories = append([]Category(nil), list...)
	sort.SliceStable(c.categories, func(i, j int) bool {
		if c.categories[i].Order != c.categories[j].Order {
			return c.categories[i].Order < c.categories[j].Order
		}
		return c.categories[i].ID < c.categories[j].ID
	})
	return nil
}

// Categories lists the declared categories in display order.
func (c *Catalog) Categories() []Category { return append([]Category(nil), c.categories...) }

// HasCategory reports whether the catalog declares this category id, so a preset
// naming a category that no longer exists is a load error instead of a silent
// empty selection at export time.
func (c *Catalog) HasCategory(id string) bool {
	_, ok := c.byCategory[id]
	return ok
}

// CategoryByID returns one declaration.
func (c *Catalog) CategoryByID(id string) (Category, bool) {
	cat, ok := c.byCategory[id]
	return cat, ok
}

// CategoryName resolves a display name, falling back to the id.
func (c *Catalog) CategoryName(id string) string {
	if cat, ok := c.byCategory[id]; ok {
		return cat.Name
	}
	if id == OtherCategory {
		return "其他"
	}
	return id
}

// EdgeByID returns the declared edge with this id.
func (c *Catalog) EdgeByID(id string) (*Edge, bool) {
	edge, ok := c.byID[strings.ToLower(id)]
	return edge, ok
}

// categoryForExtension maps a target extension to its category.
func (c *Catalog) categoryForExtension(ext string) (string, bool) {
	id, ok := c.catByExt[normalizeExt(ext)]
	return id, ok
}

// categoryForRelation maps a registry relation to its category.
func (c *Catalog) categoryForRelation(relation string) (string, bool) {
	id, ok := c.catByRel[strings.TrimSpace(relation)]
	return id, ok
}

// DeclaresSection reports whether any edge covers this extension/section pair.
func (c *Catalog) DeclaresSection(ext, section string) bool {
	set := c.sections[normalizeExt(ext)]
	if set == nil {
		return false
	}
	return set[strings.ToLower(section)]
}

// EdgeForSectionAnywhere returns the first edge that declares the given section
// for any extension. Suggestions use it as corroboration: a section already
// known to carry references elsewhere is safe to inherit.
func (c *Catalog) EdgeForSectionAnywhere(section string) *Edge {
	key := strings.ToLower(section)
	extensions := make([]string, 0, len(c.byExt))
	for ext := range c.byExt {
		extensions = append(extensions, ext)
	}
	sort.Strings(extensions)
	for _, ext := range extensions {
		if edges := c.byExt[ext][key]; len(edges) > 0 {
			return edges[0]
		}
	}
	return nil
}

// Relations lists every relation name the catalog depends on, sorted.
func (c *Catalog) Relations() []string {
	set := map[string]bool{}
	for _, e := range c.document.Edges {
		if e.Target.Kind == TargetRegistry && e.Target.Relation != "" {
			set[e.Target.Relation] = true
		}
		if cp := e.Record.countPrefixed(); cp != nil {
			for _, ref := range cp.PayloadRefs {
				if ref.Relation != "" {
					set[ref.Relation] = true
				}
			}
			for _, ref := range cp.NestedRefs {
				for _, route := range ref.Index.Routes {
					if route.Relation != "" {
						set[route.Relation] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SectionIndexPaths lists the archive entries whose sections the catalog reads
// as indices. They are dependencies of the *analysis*, so a cache that only
// watches registries would keep resolving with a stale group table.
func (c *Catalog) SectionIndexPaths() []string {
	set := map[string]bool{}
	for _, e := range c.document.Edges {
		if e.Target.Table != nil && e.Target.Table.Path != "" {
			// 表同样是"被读进缓存的另一个条目"：改了它，靠它解析出来的结果全作废。
			set[e.Target.Table.Path] = true
		}
		cp := e.Record.countPrefixed()
		if cp == nil {
			continue
		}
		for _, ref := range cp.NestedRefs {
			if ref.Index.Path != "" {
				set[ref.Index.Path] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// payloadRefFor returns the declared reference for one kind's payload slot.
func (cp *CountPrefixed) payloadRefFor(kind string, slot int) string {
	for _, ref := range cp.PayloadRefs {
		if ref.Slot == slot && strings.EqualFold(ref.Kind, kind) {
			return ref.Relation
		}
	}
	return ""
}

// countPrefixed returns the count-prefixed shape of a record, or nil.
func (r *Record) countPrefixed() *CountPrefixed {
	if r == nil {
		return nil
	}
	return r.CountPrefixed
}

// needsCountPrefixed reports whether any edge for this extension declares a
// data-driven record width, in which case the scanner walks that section as
// whole records instead of judging each token in isolation.
func (c *Catalog) needsCountPrefixed(ext, section string) *CountPrefixed {
	for _, edge := range c.EdgesIn(ext, section) {
		if cp := edge.Record.countPrefixed(); cp != nil {
			return cp
		}
	}
	return nil
}

// EdgeCount reports how many edges the catalog declares.
func (c *Catalog) EdgeCount() int { return len(c.document.Edges) }

// Sections lists the declared section names for one extension, sorted.
func (c *Catalog) Sections(ext string) []string {
	return c.sectionsForExt(normalizeExt(ext))
}

func (c *Catalog) sectionsForExt(ext string) []string {
	set := c.sections[ext]
	if set == nil {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func itoa(value int) string { return strconv.Itoa(value) }

// Document returns a copy of the underlying document.
func (c *Catalog) Document() Document {
	out := Document{Version: c.document.Version, Description: c.document.Description}
	out.Edges = append([]Edge(nil), c.document.Edges...)
	return out
}

func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	// Reject anything that is not a plausible extension so a typo in the
	// catalog fails at load time rather than silently matching nothing.
	if strings.ContainsAny(ext, "/\\ \t") || len(ext) > 8 {
		return ""
	}
	return ext
}
