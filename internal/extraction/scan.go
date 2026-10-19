package extraction

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"pvfine/internal/pvf"
)

// Scanner extracts references from archive entries using a Catalog.
type Scanner struct {
	archive   *pvf.Archive
	catalog   *Catalog
	registers *RegistrySet
	basenames map[string][]int32
	scanned   int64
	// indexes caches the section tables the catalog reads, keyed by the entry
	// path they were declared against.
	indexMu sync.Mutex
	indexes map[string]*sectionIndex
	// tables caches the key→value block tables read out of other entries.
	tables map[string]*sectionTable
	// registrations caches the archive-path → .lst rows index that export
	// fragments need, built at most once per scanner.
	registrations map[string][]registeredRow
}

// NewScanner binds an archive, a catalog and a registry set together.
func NewScanner(a *pvf.Archive, c *Catalog, rs *RegistrySet) *Scanner {
	return &Scanner{archive: a, catalog: c, registers: rs,
		indexes: map[string]*sectionIndex{}, tables: map[string]*sectionTable{}}
}

// Scanned reports how many entries have been parsed.
func (s *Scanner) Scanned() int { return int(atomic.LoadInt64(&s.scanned)) }

// Prewarm pays the one-time costs up front so a later preview or export is not
// the thing that stalls: the basename index (a full entry sweep) and every
// registry the catalog names. Callers should run it after an archive loads, off
// the interaction path; it honours cancellation between steps.
func (s *Scanner) Prewarm(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.prewarmBasenames(ctx); err != nil {
		return err
	}
	s.registers.PreloadAll(ctx)
	return nil
}

// prewarmBasenames builds the basename index incrementally so cancellation is
// possible, unlike the lazy build which must finish once it starts.
func (s *Scanner) prewarmBasenames(ctx context.Context) error {
	if s.basenames != nil {
		return nil
	}
	index := make(map[string][]int32, s.archive.FileCount()/8)
	for entry := int32(0); entry < s.archive.FileCount(); entry++ {
		if entry%4096 == 0 && ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		base := strings.ToLower(path.Base(s.archive.FullPath(entry)))
		if base == "." || base == "/" {
			continue
		}
		index[base] = append(index[base], entry)
	}
	s.basenames = index
	return nil
}

// Catalog exposes the bound edge set.
func (s *Scanner) Catalog() *Catalog { return s.catalog }

// RegistrySet exposes the bound registries for reporting.
func (s *Scanner) RegistrySet() *RegistrySet { return s.registers }

// ScanFile extracts every declared reference plus unmodeled candidates from
// one archive entry.
func (s *Scanner) ScanFile(index int32) (ScanResult, error) {
	fullPath := s.archive.FullPath(index)
	ext := strings.ToLower(path.Ext(fullPath))
	result := ScanResult{FileIndex: index, Path: fullPath}
	// 直读归档自己的 token 流。旧路径是 Text() 反编译成文本、再用 ParseScriptView
	// 重新词法，等于把 token 走两遍；token 路径只解一遍，且不必为坐标/数值构造
	// 文本（见 docs/提取中心实施计划.md 的 P2 实测）。
	view, err := s.archive.ScriptViewOf(index)
	if err != nil {
		return result, err
	}
	atomic.AddInt64(&s.scanned, 1)
	result.TextBytes = int(s.archive.File(index).DataSize)
	// 列表文件由 RegistrySet 解析，不是脚本；把它们当脚本文本扫描会把每一行
	// 注册记录都当成"未声明的路径引用"，污染缺口队列。
	if ext == ".lst" {
		return result, nil
	}

	// 计数前缀 section 必须整段按记录走：单个 token 无法判断自己落在记录前缀、
	// 载荷还是配对块里，只有把 section 的 token 连起来才能切出记录边界。
	countSections := map[int][]int{}
	for i := range view.Elements {
		el := &view.Elements[i]
		if el.Kind != pvf.ScriptElementToken {
			continue
		}
		if s.catalog.needsCountPrefixed(ext, el.Section) != nil {
			countSections[el.SectionID] = append(countSections[el.SectionID], i)
		}
	}

	for i := range view.Elements {
		el := &view.Elements[i]
		if el.Kind != pvf.ScriptElementToken {
			continue
		}
		result.Tokens++
		textBearing := tokenCarriesText(el.TokenType)
		if textBearing {
			result.Vocabulary++
		}
		if _, walked := countSections[el.SectionID]; walked {
			continue
		}
		edges := s.catalog.EdgesIn(ext, el.Section)
		if len(edges) == 0 {
			if shape := candidateShape(el); shape != "" {
				result.Unmodeled = append(result.Unmodeled, Unmodeled{
					Extension: ext, Section: el.Section, Shape: shape,
					TokenType: el.TokenType, Value: clip(el.Value),
				})
			}
			continue
		}
		consumed := false
		for _, edge := range edges {
			if !tokenTypeAllowed(edge.TokenTypes, el.TokenType) {
				continue
			}
			if !ancestorsAllowed(edge.Under, el.SectionPath) {
				continue
			}
			if ref, handled := s.eval(el, edge, fullPath); handled {
				result.Refs = append(result.Refs, ref)
				consumed = true
			}
		}
		if textBearing && consumed {
			result.Consumed++
		}
	}
	sectionIDs := make([]int, 0, len(countSections))
	for sectionID := range countSections {
		sectionIDs = append(sectionIDs, sectionID)
	}
	sort.Ints(sectionIDs)
	for _, sectionID := range sectionIDs {
		s.walkCountPrefixed(&view, countSections[sectionID], ext, &result)
	}
	return result, nil
}

// walkCountPrefixed splits one section's token ordinals into count-prefixed
// records. The shape is `<prefix…> n <item>×n`, so the count column and the
// item size make the stream self-synchronising: no source lines and no
// rendering rules are consulted. A record's leading id is the declared
// reference; item payloads are reported as candidates under a shape keyed by
// their kind, so nothing in such a section can disappear silently.
func (s *Scanner) walkCountPrefixed(view *pvf.ScriptView, ords []int, ext string, result *ScanResult) (records, consumed int) {
	if len(ords) == 0 {
		return 0, 0
	}
	section := view.Elements[ords[0]].Section
	var edge *Edge
	var cp *CountPrefixed
	for _, candidate := range s.catalog.EdgesIn(ext, section) {
		if shape := candidate.Record.countPrefixed(); shape != nil {
			edge, cp = candidate, shape
			break
		}
	}
	if edge == nil {
		return 0, 0
	}
	tok := func(at int) *pvf.ScriptElement { return &view.Elements[ords[at]] }
	malformed := func(at int, what string) {
		value := ""
		if at >= 0 && at < len(ords) {
			value = clip(tok(at).Value)
		}
		result.Unmodeled = append(result.Unmodeled, Unmodeled{
			Extension: ext, Section: section, Shape: "record-malformed",
			TokenType: 0, Value: what + " @" + strconv.Itoa(at) + " " + value,
		})
	}

	pos := 0
	for pos < len(ords) {
		if pos+cp.PrefixTokens > len(ords) {
			malformed(pos, "记录前缀不足")
			return records, pos
		}
		count, ok := parseRecordCount(tok(pos + cp.CountIndex).Value)
		if !ok {
			malformed(pos+cp.CountIndex, "计数列非整数")
			return records, pos
		}
		idElement := tok(pos)
		ref := Ref{EdgeID: edge.ID, Kind: edge.Target.Kind, Value: idElement.Value,
			Section: section, Line: -1, Target: -1}
		s.resolveDeclared(&ref, edge, idElement.Value, "", result.Path)
		result.Refs = append(result.Refs, ref)
		records++

		cursor := pos + cp.PrefixTokens
		broken := false
		// walkNested 消费一个配对块：open 是块标签所在的序数，kind 已归一化。
		// 声明过的块按记录宽度解释并把指定列送进段内索引；没声明的块把内部值
		// 全部登记成候选，否则"未建模"就退化成"静默丢弃"。
		walkNested := func(open int, kind string) (next int, ok bool) {
			end := closingBlock(ords, view, open, kind)
			if end < 0 {
				malformed(open, "配对块未闭合: "+kind)
				return open, false
			}
			if nested := cp.nestedRefFor(kind); nested != nil {
				s.emitNestedRefs(result, edge, nested, section, elementsOf(view, ords[open+1:end]))
				if close := tok(end); tokenCarriesText(close.TokenType) {
					result.Vocabulary++
					result.Consumed++
				}
				return end + 1, true
			}
			shape := "nested[" + kind + "]"
			for at := open; at <= end; at++ {
				el := tok(at)
				if at != open && tokenCarriesText(el.TokenType) {
					result.Vocabulary++
				}
				if el.TokenType != 0 || el.Value == "0" || el.Value == "-1" {
					continue
				}
				result.Unmodeled = append(result.Unmodeled, Unmodeled{
					Extension: ext, Section: section, Shape: shape,
					TokenType: el.TokenType, Value: clip(el.Value),
				})
			}
			return end + 1, true
		}
		for item := 0; item < count; item++ {
			if cursor+cp.KindIndex >= len(ords) {
				malformed(cursor, "条目越界")
				broken = true
				break
			}
			kindEl := tok(cursor + cp.KindIndex)
			kind := normalizeTag(kindEl.Value)
			// 条目里的 kind 标签被记录遍历解释过（决定是叶子还是配对块），
			// 因此算词汇已消费；配对块内部只登记词汇、不声明消费。
			if tokenCarriesText(kindEl.TokenType) {
				result.Vocabulary++
				result.Consumed++
			}
			if containsFold(cp.NestedKinds, kind) {
				next, ok := walkNested(cursor+cp.KindIndex, kind)
				if !ok {
					broken = true
					break
				}
				cursor = next
				continue
			}
			if cursor+cp.ItemTokens > len(ords) {
				malformed(cursor, "条目载荷越界")
				broken = true
				break
			}
			shape := "payload[" + kind + "]"
			for slot := 0; slot < cp.ItemTokens; slot++ {
				if slot == cp.KindIndex {
					continue
				}
				el := tok(cursor + slot)
				if el.TokenType != 0 || el.Value == "0" || el.Value == "-1" {
					continue
				}
				// 已声明的槽位是真引用：按声明的 registry 解析并像其它引用一样
				// 参与闭包；未声明的仍只是候选，绝不因为"看起来像 ID"就升级。
				if relation := cp.payloadRefFor(kind, slot); relation != "" {
					itemRef := Ref{EdgeID: edge.ID, Kind: TargetRegistry, Value: el.Value,
						Section: section, Line: -1, Target: -1}
					s.resolveDeclared(&itemRef, edge, el.Value, relation, result.Path)
					result.Refs = append(result.Refs, itemRef)
					continue
				}
				result.Unmodeled = append(result.Unmodeled, Unmodeled{
					Extension: ext, Section: section, Shape: shape,
					TokenType: el.TokenType, Value: clip(el.Value),
				})
			}
			cursor += cp.ItemTokens
		}
		if broken {
			return records, pos
		}
		// 计数之外的配对块。实测 86 的 map/amon/hell_amonhigh.map 与
		// hell_amonlow.map：`235 825 343 0 2 [trap]…[trap]…` 之后还跟着一段
		// `[hellparty]` 块，n 并不含它。按计数走会把块首标签当成下一条记录的
		// ID，整组深渊怪物就此静默丢失。标签是"已声明的配对块类型"时才接管，
		// 并同时登记一条 record-trailing-block 候选，让这个形状差异可审计。
		for !broken && cursor < len(ords) {
			head := tok(cursor)
			if !tokenCarriesText(head.TokenType) {
				break
			}
			trailing := normalizeTag(head.Value)
			if !containsFold(cp.NestedKinds, trailing) {
				break
			}
			result.Vocabulary++
			result.Consumed++
			result.Unmodeled = append(result.Unmodeled, Unmodeled{
				Extension: ext, Section: section, Shape: "record-trailing-block",
				TokenType: head.TokenType, Value: clip(head.Value),
			})
			next, ok := walkNested(cursor, trailing)
			if !ok {
				broken = true
				break
			}
			cursor = next
		}
		if broken {
			return records, pos
		}
		pos = cursor
	}
	return records, pos
}

// parseRecordCount reads a non-negative item count.
func parseRecordCount(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 6 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
		n = n*10 + int(value[i]-'0')
	}
	return n, true
}

// elementsOf projects an ordinal range back onto the view's elements.
func elementsOf(view *pvf.ScriptView, ords []int) []*pvf.ScriptElement {
	out := make([]*pvf.ScriptElement, 0, len(ords))
	for _, at := range ords {
		out = append(out, &view.Elements[at])
	}
	return out
}

// closingBlock finds the paired terminator `[/kind]` of a nested item and
// returns its ordinal, or -1 when the block never closes.
func closingBlock(ords []int, view *pvf.ScriptView, open int, kind string) int {
	wanted := "/" + kind
	for at := open + 1; at < len(ords); at++ {
		if normalizeTag(view.Elements[ords[at]].Value) == wanted {
			return at
		}
	}
	return -1
}

func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

// tokenCarriesText reports whether a token type holds text rather than a
// number. ScriptView uses 3 for bare words, 5/7 for 90US block strings, 6 for
// backticked strings and 8/10 for string-pool references in paged archives.
func tokenCarriesText(tokenType int32) bool {
	switch tokenType {
	case 3, 5, 6, 7, 8, 10:
		return true
	}
	return false
}

// resolveDeclared resolves one declared reference value according to the edge
// target. registryRelation overrides the edge relation when the value belongs
// to a record item rather than to the record itself.
// resolveAsset records a client-image reference. Some archives also carry the
// same image as an entry, and recording that index is what makes "only assets
// point here" measurable in the reverse index; either way the reference is never
// followed by a closure, because Ref.Followable excludes assets.
func (s *Scanner) resolveAsset(ref *Ref, value string) {
	ref.State = StateResolved
	ref.TargetPath = clip(value)
	index, target, ok, ambiguous := s.resolveRelative("", value, nil)
	if ok && ambiguous <= 1 {
		ref.Target = index
		ref.TargetPath = target
	}
}

func (s *Scanner) resolveDeclared(ref *Ref, edge *Edge, value, registryRelation, fromPath string) {
	value = strings.TrimSpace(value)
	switch edge.Target.Kind {
	case TargetAsset:
		s.resolveAsset(ref, value)
		return
	case TargetSectionTable:
		s.resolveSectionTable(ref, edge, value, fromPath)
		return
	case TargetEntry:
		if value == "" {
			ref.State = StateMissing
			ref.Note = "空值"
			return
		}
		index, target, ok, ambiguous := s.resolveRelative(fromPath, value, edge.Target.Roots)
		if !ok {
			ref.State = StateMissing
			ref.TargetPath = clip(target)
			if ambiguous > 1 {
				ref.State = StateUnknown
				ref.Note = fmt.Sprintf("同名条目 %d 个，无法判定目标", ambiguous)
			} else {
				ref.Note = "相对路径未命中归档条目"
			}
			return
		}
		ref.Target = index
		ref.TargetPath = target
		ref.State = StateResolved
		return
	}
	if valueIsNone(value, edge.Target.NoneValues) {
		ref.State = StateUnknown
		ref.Note = "空标识（0/-1），非引用"
		return
	}
	relation := edge.Target.Relation
	if registryRelation != "" {
		relation = registryRelation
	}
	ref.Relation = relation
	target, err := s.registers.Resolve(relation, value)
	if err != nil {
		ref.State = StateMissing
		ref.Note = err.Error()
		return
	}
	ref.Target = target.Index
	ref.TargetPath = target.Path
	if target.Index < 0 {
		ref.State = StateMissing
		ref.Note = target.Reason
		return
	}
	ref.State = StateResolved
}

// eval decides whether one element is a reference position under one edge and
// resolves it. A false second return means the element is not a reference
// position for this edge, such as a coordinate column inside a wider record.
func (s *Scanner) eval(el *pvf.ScriptElement, edge *Edge, fullPath string) (Ref, bool) {
	position := el.Index
	if rec := edge.Record; rec != nil && !positionAllowed(rec, position) {
		return Ref{}, false
	}

	ref := Ref{EdgeID: edge.ID, Kind: edge.Target.Kind, Value: el.Value,
		Section: el.Section, Line: -1, Target: -1}
	value := strings.TrimSpace(el.Value)
	if value == "" {
		return ref, false
	}
	// 动态模板值对任何 target 形态都是判不了的，asset 边也一样——把 `%02d` 记成
	// "已解析的客户端资产"是假的成功。
	if isDynamicPath(value) {
		ref.State = StateUnknown
		ref.Note = "动态路径，无法静态判定"
		ref.TargetPath = clip(value)
		return ref, true
	}
	switch edge.Target.Kind {
	case TargetAsset:
		s.resolveAsset(&ref, value)
		return ref, true
	case TargetSectionTable:
		s.resolveSectionTable(&ref, edge, value, fullPath)
		return ref, true
	case TargetRegistry:
		if valueIsNone(el.Value, edge.Target.NoneValues) {
			return ref, false
		}
		target, err := s.registers.Resolve(edge.Target.Relation, strings.TrimSpace(el.Value))
		if err != nil {
			ref.State = StateMissing
			ref.Note = err.Error()
			return ref, true
		}
		ref.Target = target.Index
		ref.TargetPath = target.Path
		if target.Index < 0 {
			ref.State = StateMissing
			ref.Note = target.Reason
			return ref, true
		}
		ref.State = StateResolved
		return ref, true
	default:
		if looksLikeAssetPath(value) {
			ref.State = StateUnknown
			ref.Note = "客户端资产引用，该 section 应声明为 asset 边"
			ref.TargetPath = clip(value)
			return ref, true
		}
		if !looksLikeArchiveEntry(value) {
			return ref, false
		}
		index, target, ok, ambiguous := s.resolveRelative(fullPath, value, edge.Target.Roots)
		if !ok {
			ref.State = StateMissing
			ref.TargetPath = clip(target)
			if ambiguous > 1 {
				ref.State = StateUnknown
				ref.Note = fmt.Sprintf("同名条目 %d 个，无法判定目标", ambiguous)
			} else {
				ref.Note = "相对路径未命中归档条目"
			}
			return ref, true
		}
		if len(edge.Target.ExpectExt) > 0 && !extAllowed(edge.Target.ExpectExt, target) {
			// The reference resolved to a real entry that the edge expected a
			// different kind of. That is a catalog bug or a new revision's
			// change of shape, never a reason to hide the reference.
			ref.State = StateUnknown
			ref.TargetPath = target
			ref.Note = "解析到条目但扩展名不在 expectExt 内，边声明可能有误"
			return ref, true
		}
		ref.Target = index
		ref.TargetPath = target
		ref.State = StateResolved
		return ref, true
	}
}

// resolveRelative tries owner-relative, the `(r)` localized sibling used by the
// Chinese and Korean clients, then the archive root, then any root the edge
// declares, and finally — for a bare file name with no directory component — the
// archive's basename index, picking the candidate that shares the longest
// directory prefix with the referrer.
// The bare-name path exists because e.g. an .aic lists `[ai pattern]` as
// `Action.ai` while the entry actually lives in a sibling `ai/` directory.
// Declared roots come last on purpose: every value that already resolves today
// keeps resolving the same way, and the new rung only fires where nothing else
// matched.
func (s *Scanner) resolveRelative(fromPath, value string, roots []string) (int32, string, bool, int) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(value, "/")
	owner := path.Dir(fromPath)
	primary := path.Join(owner, value)
	candidates := []string{primary, path.Join(path.Dir(primary), "(r)"+path.Base(primary))}
	if root := path.Clean(value); root != "." && !strings.Contains(root, "..") {
		candidates = append(candidates, root)
	}
	for _, declared := range roots {
		normalized := strings.ReplaceAll(strings.TrimSpace(declared), "\\", "/")
		var candidate string
		if strings.HasPrefix(normalized, "../") {
			// 相对 owner 的基准：`.key` 的值 `key/dummy.ptl` 住在引用它的 `.aic`
			// 目录里，而不是 `.key` 自己的目录，实测 772/773 只有这一档能命中。
			candidate = path.Join(owner, normalized, value)
		} else {
			candidate = path.Join("/", normalized, value)
			candidate = strings.TrimPrefix(candidate, "/")
		}
		clean := path.Clean(candidate)
		if clean == value || strings.HasPrefix(clean, "../") || strings.Contains(clean, "..") {
			continue
		}
		candidates = append(candidates, clean, path.Join(path.Dir(clean), "(r)"+path.Base(clean)))
	}
	for _, candidate := range candidates {
		if index, ok := s.archive.Find(candidate); ok {
			return index, s.archive.FullPath(index), true, 0
		}
	}
	if !strings.Contains(value, "/") {
		index, ambiguous := s.resolveByBasename(owner, value)
		if index >= 0 {
			return index, s.archive.FullPath(index), true, 0
		}
		return -1, primary, false, ambiguous
	}
	return -1, primary, false, 0
}

// resolveByBasename consults a lazily built basename index. The index is only
// constructed when a direct lookup has already missed, so the common case pays
// nothing for it.
func (s *Scanner) resolveByBasename(ownerDir, value string) (int32, int) {
	wanted := strings.ToLower(path.Base(value))
	byName := s.nameIndex()
	candidates := byName[wanted]
	if len(candidates) == 0 {
		return -1, 0
	}
	if len(candidates) == 1 {
		return candidates[0], 1
	}
	best, bestScore := int32(-1), -1
	ties := 0
	for _, candidate := range candidates {
		score := commonDirPrefix(ownerDir, path.Dir(s.archive.FullPath(candidate)))
		if score > bestScore {
			best, bestScore, ties = candidate, score, 1
		} else if score == bestScore {
			ties++
		}
	}
	if ties > 1 {
		return -1, ties
	}
	return best, 1
}

// nameIndex builds (and memoises) a lowercased basename -> entry index map.
func (s *Scanner) nameIndex() map[string][]int32 {
	if s.basenames != nil {
		return s.basenames
	}
	_ = s.prewarmBasenames(nil)
	return s.basenames
}

// commonDirPrefix counts shared path segments, used to prefer the nearest
// same-named entry over one in an unrelated subtree.
func commonDirPrefix(a, b string) int {
	left, right := strings.Split(strings.ToLower(a), "/"), strings.Split(strings.ToLower(b), "/")
	shared := 0
	for shared < len(left) && shared < len(right) && left[shared] == right[shared] {
		shared++
	}
	return shared
}

// positionAllowed reports whether a field position inside a record holds a
// reference, wrapping the stream position by the declared record width.
func positionAllowed(rec *Record, position int) bool {
	if rec.Width <= 1 {
		return true
	}
	offset := position % rec.Width
	if len(rec.RefOffsets) == 0 {
		return offset == 0
	}
	return containsInt(rec.RefOffsets, offset)
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func tokenTypeAllowed(allowed []int32, got int32) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, want := range allowed {
		if want == got {
			return true
		}
	}
	return false
}

func ancestorsAllowed(under []string, sectionPath []string) bool {
	if len(under) == 0 {
		return true
	}
	cursor := 0
	for _, want := range under {
		want = normalizeTag(want)
		found := false
		for ; cursor < len(sectionPath); cursor++ {
			if normalizeTag(sectionPath[cursor]) == want {
				found = true
				cursor++
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func valueIsNone(value string, none []string) bool {
	if len(none) == 0 {
		none = DefaultNoneValues
	}
	value = strings.TrimSpace(value)
	for _, candidate := range none {
		if value == candidate {
			return true
		}
	}
	return false
}

func extAllowed(expected []string, target string) bool {
	ext := strings.ToLower(path.Ext(target))
	for _, want := range expected {
		if strings.ToLower(want) == ext {
			return true
		}
	}
	return false
}

func normalizeTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "`")
	s = strings.Trim(s, "[]")
	return strings.TrimSpace(s)
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx] + "~"
	}
	if len(s) > 72 {
		return s[:72]
	}
	return s
}
