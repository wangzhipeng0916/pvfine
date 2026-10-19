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
	// Via records the form that resolved: "path", "relation" or "id". Reports
	// need it because "this seed came from the 怪物 table" is a fact the user
	// should see, not something the resolver keeps to itself.
	Via      string
	Relation string
}

// ResolveSeed turns one user-supplied seed into an archive entry.
//
// Paths were the only form ever implemented, though ID seeds are what a MOD
// author usually has in hand (a row number in a table, not a file path). A bare
// ID is accepted only when exactly one relation claims it: two tables using the
// same number is ordinary in these archives, and silently choosing one would
// invent a dependency nobody asked for.
func ResolveSeed(archive *pvf.Archive, registers *RegistrySet, seed string) (SeedHit, error) {
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
	if registers == nil || !isBareID(seed) {
		return SeedHit{Path: seed}, fmt.Errorf(
			"归档里没有这个条目：%s（可写成条目路径、关系名:ID，或纯 ID）", seed)
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
