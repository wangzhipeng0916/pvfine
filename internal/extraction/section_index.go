package extraction

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"pvfine/internal/pvf"
)

// sectionMember is one member of an indexed group: the value to look up plus
// the kind tag that says which registry owns it.
type sectionMember struct {
	Value string
	Kind  string
}

// sectionGroup is one record of a section index. Tier keeps the block's own
// label verbatim — the knowledge base warns against promoting a display name to
// a column meaning, so it is stored but never interpreted.
type sectionGroup struct {
	Tier    string
	Members []sectionMember
}

// sectionIndex is the table read out of one entry's sections. It is cached per
// scanner because one closure can ask for the same group many times.
type sectionIndex struct {
	// absent says the index entry itself is not in this archive, which makes
	// every group reference missing rather than silently empty.
	absent    bool
	entry     int32
	groups    map[int32]sectionGroup
	records   int
	malformed int
}

// sectionIndexFor loads (once) the index a nested declaration points at.
func (s *Scanner) sectionIndexFor(spec SectionIndex) *sectionIndex {
	key := strings.ToLower(path.Clean(spec.Path))
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if cached, ok := s.indexes[key]; ok {
		return cached
	}
	built := buildSectionIndex(s.archive, spec)
	s.indexes[key] = built
	return built
}

// buildSectionIndex pairs the index section's keys with the member section's
// value stream, positionally: `[group index] n` is followed by `[group]` whose
// body is (value, kind) pairs. Alignment is checked by length, so a revision
// that changes the member width surfaces as malformed instead of shifting every
// kind by one column.
func buildSectionIndex(a *pvf.Archive, spec SectionIndex) *sectionIndex {
	out := &sectionIndex{entry: -1, groups: map[int32]sectionGroup{}}
	index, ok := a.Find(spec.Path)
	if !ok {
		out.absent = true
		return out
	}
	out.entry = index
	view, err := a.ScriptViewOf(index)
	if err != nil {
		out.absent = true
		return out
	}
	wantIndex := strings.ToLower(spec.IndexSection)
	wantMembers := strings.ToLower(spec.MemberSection)
	var (
		cur      int32
		have     bool
		integers []string
		tier     string
	)
	closeGroup := func() {
		if !have {
			return
		}
		group := sectionGroup{Tier: tier}
		if len(integers)%spec.MemberWidth != 0 {
			out.malformed++
		}
		for at := 0; at+spec.MemberWidth <= len(integers); at += spec.MemberWidth {
			row := integers[at : at+spec.MemberWidth]
			group.Members = append(group.Members, sectionMember{
				Value: strings.TrimSpace(row[0]),
				Kind:  strings.TrimSpace(row[spec.KindOffset]),
			})
		}
		existing, dup := out.groups[cur]
		if !dup || len(group.Members) > len(existing.Members) {
			out.groups[cur] = group
		}
		if len(group.Members) > 0 {
			out.records++
		}
		integers, tier, have = nil, "", false
	}
	for _, el := range view.Elements {
		section := strings.ToLower(el.Section)
		switch section {
		case wantIndex:
			if el.TokenType != 0 {
				continue
			}
			id, err := strconv.Atoi(strings.TrimSpace(el.Value))
			if err != nil {
				continue
			}
			closeGroup()
			cur, have = int32(id), true
		case wantMembers:
			if !have {
				continue
			}
			if tokenCarriesText(el.TokenType) {
				// `[group]` 标签自身也是文本 token，别把它当档位名收下来。
				if normalizeTag(el.Value) == wantMembers {
					continue
				}
				if tier == "" {
					tier = strings.Trim(el.Value, "`")
				}
				continue
			}
			if el.TokenType == 0 {
				integers = append(integers, el.Value)
			}
		}
	}
	closeGroup()
	return out
}

// emitNestedRefs turns one paired block into references. The declared column
// resolves through the section index and fans out to that group's members;
// every other column keeps its place in the candidate queue, so declaring a
// block never makes a value less visible than it was.
func (s *Scanner) emitNestedRefs(result *ScanResult, edge *Edge, nested *NestedRef,
	section string, body []*pvf.ScriptElement) {
	if nested.Width < 1 {
		return
	}
	// 记录按"值列"对齐：文本 token（标签、档位名）解释掉但不参与步长，否则
	// 版本里多一个字符串就会把后面所有列一起错位。
	values := make([]*pvf.ScriptElement, 0, len(body))
	for _, el := range body {
		if tokenCarriesText(el.TokenType) {
			result.Vocabulary++
			result.Consumed++
			continue
		}
		values = append(values, el)
	}
	shape := "nested[" + nested.Kind + "]"
	if len(values)%nested.Width != 0 {
		result.Unmodeled = append(result.Unmodeled, Unmodeled{
			Extension: resultExt(result), Section: section, Shape: "record-malformed",
			TokenType: 0,
			Value: "配对块值数 " + strconv.Itoa(len(values)) + " 不能被记录宽度 " +
				strconv.Itoa(nested.Width) + " 整除",
		})
	}
	for at, el := range values {
		if at%nested.Width != nested.Offset {
			result.Unmodeled = append(result.Unmodeled, Unmodeled{
				Extension: resultExt(result), Section: section,
				Shape: shape, TokenType: el.TokenType, Value: clip(el.Value),
			})
			continue
		}
		if el.TokenType != 0 || el.Value == "0" || el.Value == "-1" {
			continue
		}
		s.resolveSectionGroup(result, edge, nested, section, el.Value)
	}
}

// resolveSectionGroup expands one index key into the references it implies: the
// index entry itself, then each member routed to the registry its kind says.
func (s *Scanner) resolveSectionGroup(result *ScanResult, edge *Edge, nested *NestedRef,
	section, value string) {
	idx := s.sectionIndexFor(nested.Index)
	groupRef := Ref{EdgeID: edge.ID, Kind: TargetSectionIndex, Value: value,
		Section: section, Line: -1, Target: -1,
		TargetPath: nested.Index.Path + " [" + nested.Index.IndexSection + "]"}
	if idx.absent {
		groupRef.State = StateMissing
		groupRef.Note = "段内索引条目不存在: " + nested.Index.Path
		result.Refs = append(result.Refs, groupRef)
		return
	}
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		groupRef.State = StateUnknown
		groupRef.Note = "组号不是整数"
		result.Refs = append(result.Refs, groupRef)
		return
	}
	group, ok := idx.groups[int32(id)]
	if !ok {
		// 知识库要求这一类异常必须报告："map 引用但 index 缺失"。
		groupRef.State = StateMissing
		groupRef.Note = "索引里没有该组"
		result.Refs = append(result.Refs, groupRef)
		return
	}
	if len(group.Members) == 0 {
		groupRef.State = StateMissing
		groupRef.Note = "索引有该组但没有成员"
		result.Refs = append(result.Refs, groupRef)
		return
	}
	groupRef.Target = idx.entry
	groupRef.TargetPath = nested.Index.Path
	groupRef.State = StateResolved
	result.Refs = append(result.Refs, groupRef)

	for _, member := range group.Members {
		ref := Ref{EdgeID: edge.ID, Kind: TargetRegistry, Value: member.Value,
			Section: section, Line: -1, Target: -1}
		relation, routed := nested.Index.relationFor(member.Kind)
		if !routed {
			// 未声明归属的 kind 保持 unknown，不回退到"猜一张表"。
			ref.State = StateUnknown
			ref.Note = "成员 kind=" + member.Kind + " 未声明归属"
			result.Refs = append(result.Refs, ref)
			continue
		}
		ref.Relation = relation
		if member.Value == "" || member.Value == "0" || member.Value == "-1" {
			ref.State = StateUnknown
			ref.Note = "空成员标识，非引用"
			result.Refs = append(result.Refs, ref)
			continue
		}
		target, err := s.registers.Resolve(relation, member.Value)
		if err != nil || target.Index < 0 {
			ref.State = StateMissing
			ref.TargetPath = clip(target.Path)
			ref.Note = target.Reason
			if ref.Note == "" {
				ref.Note = "registry 无此 ID"
			}
			result.Refs = append(result.Refs, ref)
			continue
		}
		ref.Target = target.Index
		ref.TargetPath = target.Path
		ref.State = StateResolved
		result.Refs = append(result.Refs, ref)
	}
}

// resultExt recovers the extension the scan is working on, so nested candidates
// keep the same aggregate key as every other candidate in that entry.
func resultExt(result *ScanResult) string {
	return strings.ToLower(path.Ext(result.Path))
}

// sectionTable is a parsed key→value block table from another entry. Like the
// section index it is cached per scanner, and an absent file is a state rather
// than an empty map so every lookup can say "表不存在" instead of "没有这个编号".
type sectionTable struct {
	absent    bool
	entry     int32
	rows      map[int32]string
	blocks    int
	malformed int
	duplicate int
}

// sectionTableFor loads (once) the table an edge points at. The cache key
// includes the section because one entry can hold several block tables.
func (s *Scanner) sectionTableFor(spec *SectionTable) *sectionTable {
	key := "\x00table\x00" + strings.ToLower(path.Clean(spec.Path)) + "\x00" + strings.ToLower(spec.Section)
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if cached, ok := s.tables[key]; ok {
		return cached
	}
	built := buildSectionTable(s.archive, spec)
	s.tables[key] = built
	return built
}

// buildSectionTable splits the section's token stream into blocks and keeps the
// declared key/value columns. Block boundaries come from tag tokens or from
// leaving the section: repeated `[x]…[/x]` blocks are not separated by anything
// else in the token stream, and the measured file puts the set number, its
// `.equ` path and a display name in the first three value tokens, with a
// variable number of part rows below them. Fixed-width slicing would drift as
// soon as a set has a different number of parts, so the split is structural.
func buildSectionTable(a *pvf.Archive, spec *SectionTable) *sectionTable {
	out := &sectionTable{entry: -1, rows: map[int32]string{}}
	index, ok := a.Find(spec.Path)
	if !ok {
		out.absent = true
		return out
	}
	out.entry = index
	view, err := a.ScriptViewOf(index)
	if err != nil {
		out.absent = true
		return out
	}
	target := strings.ToLower(spec.Section)
	group := make([]pvf.ScriptElement, 0, 8)
	flush := func() {
		if len(group) == 0 {
			return
		}
		out.blocks++
		if len(group) <= spec.ValueOffset {
			out.malformed++
			return
		}
		id, err := strconv.Atoi(strings.TrimSpace(group[spec.KeyOffset].Value))
		value := strings.TrimSpace(group[spec.ValueOffset].Value)
		if err != nil || value == "" {
			out.malformed++
			return
		}
		if _, exists := out.rows[int32(id)]; exists {
			// 同一编号出现两次时保留第一次：后者往往是活动表之外的历史块，
			// 客户端按加载顺序覆盖，而我们无法从数据里判断哪个是权威。
			out.duplicate++
			return
		}
		out.rows[int32(id)] = value
	}
	for _, element := range view.Elements {
		leaf := ""
		if count := len(element.SectionPath); count > 0 {
			leaf = strings.ToLower(element.SectionPath[count-1])
		}
		if element.TokenType == 3 || leaf != target {
			flush()
			group = group[:0]
			continue
		}
		group = append(group, element)
	}
	flush()
	return out
}

// resolveSectionTable turns one block key into the path that block declares,
// then resolves that path with the same ladder an entry edge uses.
func (s *Scanner) resolveSectionTable(ref *Ref, edge *Edge, value, fromPath string) {
	spec := edge.Target.Table
	ref.Kind = TargetSectionTable
	table := s.sectionTableFor(spec)
	if table.absent {
		ref.State = StateMissing
		ref.Note = "表条目不存在: " + spec.Path
		return
	}
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		ref.State = StateUnknown
		ref.Note = "表编号不是整数"
		return
	}
	target, ok := table.rows[int32(id)]
	if !ok {
		ref.State = StateMissing
		ref.TargetPath = spec.Path + " [" + spec.Section + "] 编号 " + value
		ref.Note = "表里没有该编号（表内 " + strconv.Itoa(table.blocks) + " 块，形状异常 " +
			strconv.Itoa(table.malformed) + " 块）"
		return
	}
	index, resolved, found, ambiguous := s.resolveRelative(fromPath, target, edge.Target.Roots)
	if !found {
		ref.State = StateMissing
		ref.TargetPath = clip(target)
		if ambiguous > 1 {
			ref.State = StateUnknown
			ref.Note = fmt.Sprintf("表给出的路径同名 %d 个，无法判定", ambiguous)
		} else {
			ref.Note = "表给出的路径不在归档里"
		}
		return
	}
	ref.Target = index
	ref.TargetPath = resolved
	ref.State = StateResolved
	ref.Note = "取自 " + spec.Path + " [" + spec.Section + "]；表文件本身不跟随，展开它等于把整张表的每一套都拉进包"
}
