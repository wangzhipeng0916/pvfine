package extraction

import (
	"fmt"
	"strings"

	"pvfine/internal/pvf"
)

// SeedHit is one resolved seed: which form the user typed and what it landed on.
type SeedHit struct {
	Index int32
	Path  string
	// Via records the form that resolved: "path", "relation", "id" or "list"
	// (a pasted .lst row). Reports need it because "this seed came from the
	// 怪物 table" is a fact the user should see, not something the resolver
	// keeps to itself.
	Via      string
	Relation string
}

// ResolveSeed turns one user-supplied seed into an archive entry, with no
// declared table.
func ResolveSeed(archive *pvf.Archive, registers *RegistrySet, seed string) (SeedHit, error) {
	return ResolveSeedIn(archive, registers, seed, "")
}

// ResolveSeedIn resolves one seed line; table names the registry a pasted id
// list belongs to, empty when the caller declared none.
//
// Four forms, tried in this order so each one's failure stays legible: whole
// line as an entry path (tried first so a path containing spaces keeps working),
// "关系名:ID", a pasted .lst row whose columns are scanned left to right for the
// first path this archive has, and a bare ID. Paths were the only form ever
// implemented, though ID seeds are what a MOD author usually has in hand (a row
// number in a table, not a file path).
//
// A bare ID is accepted without a declared table only when exactly one relation
// claims it: two tables using the same number is ordinary in these archives, and
// silently choosing one would invent a dependency nobody asked for. With a
// declared table there is nothing to guess, so the lookup goes straight through
// it — and a miss is reported as a miss of that table rather than silently
// trying the others.
func ResolveSeedIn(archive *pvf.Archive, registers *RegistrySet, seed, table string) (SeedHit, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return SeedHit{}, fmt.Errorf("种子为空")
	}
	if registers != nil {
		if relation, id, ok := registers.matchRelationID(seed); ok {
			target, err := registers.Resolve(relation, id)
			if err != nil {
				return SeedHit{Path: seed}, err
			}
			if target.Index < 0 {
				return SeedHit{Path: target.Path}, fmt.Errorf("%s:%s 解析不出条目（%s）", relation, id, target.Reason)
			}
			return SeedHit{Index: target.Index, Path: archive.FullPath(target.Index),
				Via: "relation", Relation: relation}, nil
		}
	}
	if index, ok := archive.Find(seed); ok {
		return SeedHit{Index: index, Path: archive.FullPath(index), Via: "path"}, nil
	}
	if columns := strings.Fields(seed); len(columns) > 1 {
		return resolveListRow(archive, seed, columns)
	}
	if registers == nil || !isBareID(seed) {
		return SeedHit{Path: seed}, fmt.Errorf(
			"归档里没有这个条目：%s（可写成条目路径、关系名:ID、纯 ID，或粘贴 lst 片段行）", seed)
	}
	if table != "" {
		relation, ok := registers.canonicalRelation(table)
		if !ok {
			return SeedHit{Path: seed}, fmt.Errorf("未知的清单所属表：%s（可选：%s）",
				table, strings.Join(registers.Relations(), "、"))
		}
		target, err := registers.Resolve(relation, seed)
		if err != nil {
			return SeedHit{Path: seed}, err
		}
		if target.Index < 0 {
			return SeedHit{Path: seed}, fmt.Errorf("%s 表里没有可用的 ID %s（%s）", relation, seed, target.Reason)
		}
		return SeedHit{Index: target.Index, Path: archive.FullPath(target.Index),
			Via: "relation", Relation: relation}, nil
	}
	hits := []SeedHit{}
	for _, relation := range registers.Relations() {
		target, err := registers.Resolve(relation, seed)
		if err != nil || target.Index < 0 {
			continue
		}
		hits = append(hits, SeedHit{Index: target.Index, Path: archive.FullPath(target.Index),
			Via: "id", Relation: relation})
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return SeedHit{Path: seed}, fmt.Errorf(
			"所有登记表里都没有 ID %s；已知关系可写成 关系名:%s", seed, seed)
	default:
		names := make([]string, 0, len(hits))
		for _, hit := range hits {
			names = append(names, hit.Relation)
		}
		return SeedHit{Path: seed}, fmt.Errorf(
			"ID %s 同时属于 %s，请写明关系（例如 %s:%s）", seed, strings.Join(names, "、"), names[0], seed)
	}
}

// matchRelationID recognizes the "关系名:ID" form by matching the left side
// against the relations this set actually declares, so a path that happens to
// contain a colon is never mistaken for a registry lookup.
func (rs *RegistrySet) matchRelationID(seed string) (string, string, bool) {
	at := strings.LastIndex(seed, ":")
	if at <= 0 || at == len(seed)-1 {
		return "", "", false
	}
	left := strings.TrimSpace(seed[:at])
	id := strings.TrimSpace(seed[at+1:])
	if id == "" || strings.Contains(left, "/") || strings.Contains(left, "\\") {
		return "", "", false
	}
	for relation := range rs.specs {
		if strings.EqualFold(relation, left) {
			return relation, id, true
		}
	}
	return "", "", false
}

// canonicalRelation resolves a caller-supplied table name to the spelling this
// set declares, so "装备" from a dropdown and a differently-cased typing both
// work; an unknown name is reported with the real ones instead of a silent
// fallback to the ambiguous path.
func (rs *RegistrySet) canonicalRelation(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for relation := range rs.specs {
		if strings.EqualFold(relation, name) {
			return relation, true
		}
	}
	return "", false
}

// resolveListRow resolves a pasted .lst row: columns are scanned left to right
// and the first one that names an entry in this archive wins, so a row rendered
// with extra columns, a different column order or a wrapped token still lands on
// its file. A column may span several whitespace fields because real file names
// contain spaces (`ring_a pearl_of_mamedia.equ` is a registered row of the 装备
// table), so consecutive fields are also tried joined back together. Nothing
// here falls back to an ID — a path column this archive does not have may mean
// the row came from another revision, and resolving the neighbouring number
// instead would silently point at a different file.
func resolveListRow(archive *pvf.Archive, line string, columns []string) (SeedHit, error) {
	try := func(candidate string) (SeedHit, bool) {
		candidate = strings.Trim(candidate, "`\"")
		if candidate == "" {
			return SeedHit{}, false
		}
		if index, ok := archive.Find(candidate); ok {
			return SeedHit{Index: index, Path: archive.FullPath(index), Via: "list"}, true
		}
		// 工具输出的路径可能带 Windows 分隔符；归档内部一律用 `/`。
		if normalized := strings.ReplaceAll(candidate, "\\", "/"); normalized != candidate {
			if index, ok := archive.Find(normalized); ok {
				return SeedHit{Index: index, Path: archive.FullPath(index), Via: "list"}, true
			}
		}
		return SeedHit{}, false
	}
	for from := 0; from < len(columns); from++ {
		for to := from + 1; to <= len(columns); to++ {
			if hit, ok := try(strings.Join(columns[from:to], " ")); ok {
				return hit, nil
			}
		}
	}
	return SeedHit{Path: line}, fmt.Errorf(
		"清单行里没有归档中存在的条目路径：%s（按列从左往右取第一个能命中的路径）", line)
}

func isBareID(seed string) bool {
	if seed == "" {
		return false
	}
	for i := 0; i < len(seed); i++ {
		if seed[i] < '0' || seed[i] > '9' {
			return false
		}
	}
	return true
}
