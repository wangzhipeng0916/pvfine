package extraction

import (
	"sort"
	"strconv"
	"strings"
)

// RegistryFragmentDir is where registration fragments land. They are subsets,
// not registries: a .lst must stay complete or the game's loader breaks, so
// fragments never take a .lst name and never sit where an import would treat
// them as one.
const RegistryFragmentDir = "_registries"

// RegistryFragment carries the .lst rows the extraction actually touched, per
// concrete table. This mirrors what the reference tool emitted as
// 「以下内容加入PVF【monster/monster.lst】文件内」: the id→path lines for every
// registry id the content used, so the mapping travels with the extraction
// instead of having to be looked up again in the original archive.
type RegistryFragment struct {
	Relation string
	ListPath string
	// Rows are the .lst lines as they appear in the source table, sorted.
	Rows []string
	// MissingIDs are ids the same relation was asked for but the table does not
	// hold — the unregistered case, kept next to the fragment so a gap is part
	// of the same artifact rather than a separate report nobody cross-reads.
	MissingIDs []string
}

// Dest returns the fragment's path relative to the export tree. The name keeps
// the table's own extension and adds .fragment, so `monster/monster.lst`
// becomes `_registries/monster.lst.fragment` — obviously derived from the table
// and just as obviously not a table.
func (f RegistryFragment) Dest() string {
	base := f.ListPath
	if idx := strings.LastIndexAny(base, "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	return RegistryFragmentDir + "/" + fragmentFileName(base)
}

// fragmentFileName keeps interior dots (they say which table this came from)
// while refusing separators and leading dots, so the name can never traverse.
func fragmentFileName(base string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\t", "_")
	out := replacer.Replace(strings.TrimSpace(base))
	out = strings.TrimLeft(out, ". ")
	out = strings.TrimRight(out, " ")
	if out == "" {
		return "registry.fragment"
	}
	return out + ".fragment"
}

// BuildRegistryFragments collects the involved rows of every registry the
// exported set referenced. Only ids the content actually used show up, so the
// fragment stays small even when the table is enormous.
//
// It re-reads the exported entries rather than walking the closure's edges: a
// closure keeps only followable links, so an id that is absent from its table
// would vanish from the very artifact whose job is to surface it. Judging by
// the table (not by the reference state) also gets the subtle case right — a
// registered id whose target file is missing still has a row, and belongs in
// the fragment as a row.
func BuildRegistryFragments(scanner *Scanner, exported []int32) []RegistryFragment {
	if scanner == nil || scanner.Catalog() == nil || scanner.RegistrySet() == nil {
		return nil
	}
	catalog, registers := scanner.Catalog(), scanner.RegistrySet()
	type key struct{ list, id string }
	rows := map[key]bool{}
	missing := map[string]map[string]bool{}
	listOfRelation := map[string][]LeafRegistry{}
	relationOfList := map[string]string{}
	for _, index := range exported {
		scan, err := scanner.ScanFile(index)
		if err != nil {
			continue
		}
		for _, ref := range scan.Refs {
			if ref.Kind != TargetRegistry || ref.Value == "" {
				continue
			}
			// 引用自己带的 relation 优先：载荷槽与配对块成员解析的表，和产生它的
			// 边的目标表可以不是同一张，按边查会把行找错表。
			relation := ref.Relation
			if relation == "" {
				edge, ok := catalog.EdgeByID(ref.EdgeID)
				if !ok {
					continue
				}
				relation = edge.Target.Relation
			}
			if relation == "" {
				continue
			}
			leaves, cached := listOfRelation[relation]
			if !cached {
				leaves = registers.LeafRegistries(relation)
				listOfRelation[relation] = leaves
				for _, leaf := range leaves {
					relationOfList[leaf.ListPath] = relation
				}
			}
			id := strings.TrimSpace(ref.Value)
			placed := false
			for _, leaf := range leaves {
				if registered, ok := leaf.Rows[id]; ok {
					rows[key{leaf.ListPath, id + "\t`" + registered + "`"}] = true
					placed = true
				}
			}
			if !placed && len(leaves) > 0 {
				if missing[relation] == nil {
					missing[relation] = map[string]bool{}
				}
				missing[relation][id] = true
			}
		}
	}
	byList := map[string][]string{}
	for k := range rows {
		byList[k.list] = append(byList[k.list], k.id)
	}
	lists := make([]string, 0, len(byList))
	for list := range byList {
		lists = append(lists, list)
	}
	sort.Strings(lists)
	fragments := make([]RegistryFragment, 0, len(lists))
	for _, list := range lists {
		lines := byList[list]
		sort.Slice(lines, func(i, j int) bool { return lessRegistryID(lines[i], lines[j]) })
		fragments = append(fragments, RegistryFragment{
			Relation: relationOfList[list], ListPath: list, Rows: lines,
		})
	}
	// 未登记的 ID 挂在对应 relation 的片段上；该 relation 若一条行都没命中，
	// 也要给它一个片段，否则"全都没登记"会整块消失。
	for relation, ids := range missing {
		list := ""
		for _, leaf := range listOfRelation[relation] {
			list = leaf.ListPath
			break
		}
		appended := false
		for i := range fragments {
			if fragments[i].ListPath == list {
				fragments[i].MissingIDs = sortedIDSet(ids)
				appended = true
				break
			}
		}
		if !appended {
			fragments = append(fragments, RegistryFragment{
				Relation: relation, ListPath: list, MissingIDs: sortedIDSet(ids),
			})
		}
	}
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].ListPath < fragments[j].ListPath })
	return fragments
}

// lessRegistryID orders ids numerically when both are numbers, so a fragment
// reads like the table it came from instead of like a string dump.
func lessRegistryID(a, b string) bool {
	left := strings.SplitN(a, "\t", 2)[0]
	right := strings.SplitN(b, "\t", 2)[0]
	leftNum, leftErr := strconv.ParseInt(left, 10, 64)
	rightNum, rightErr := strconv.ParseInt(right, 10, 64)
	if leftErr == nil && rightErr == nil {
		if leftNum != rightNum {
			return leftNum < rightNum
		}
		return a < b
	}
	return a < b
}

func sortedIDSet(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return lessRegistryID(out[i], out[j]) })
	return out
}
