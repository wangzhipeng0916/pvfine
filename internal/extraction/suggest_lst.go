package extraction

import (
	"path"
	"sort"
	"strings"
)

// RegistrationSuggestion proposes adding one extracted entry to the registry
// that should list it. It is deliberately a *suggestion*: writing a registry
// changes what the game loads, so the report states the evidence and the caller
// decides.
type RegistrationSuggestion struct {
	// Relation is the registry name, e.g. 被动对象.
	Relation string
	// ListPath is the .lst file the entry would be added to.
	ListPath string
	// EntryPath is the unregistered archive path.
	EntryPath string
	// ReferencedBy is the exported entry that references it, plus the edge id,
	// so every suggestion can be traced to a real usage rather than to a guess.
	ReferencedBy string
	EdgeID       string
}

// SuggestRegistrations finds entries in the exported set that are referenced
// from inside the set, belong (by extension) to a registry, and are missing
// from that registry.
//
// Entries reached *through* a registry are registered by construction, so the
// interesting ones arrive by path reference — exactly the case where a missing
// registration is a real gap. Scope is the exported set only: suggesting
// registrations for files nobody referenced would be noise.
func SuggestRegistrations(registers *RegistrySet, closure *Closure, exported []int32) ([]RegistrationSuggestion, error) {
	if registers == nil || closure == nil {
		return nil, nil
	}
	members := make(map[int32]bool, len(exported))
	for _, index := range exported {
		members[index] = true
	}
	// Which registries could own a file with this extension, according to the
	// rows those registries already contain.
	byExtension := map[string][]string{}
	listPaths := map[string]string{}
	pathIndexes := map[string]map[string]string{}
	for _, relation := range registers.Relations() {
		listPath, err := registers.ListPath(relation)
		if err != nil {
			continue
		}
		index, err := registers.PathIndex(relation)
		if err != nil {
			continue
		}
		pathIndexes[relation] = index
		listPaths[relation] = listPath
		for registered := range index {
			ext := strings.ToLower(path.Ext(registered))
			if ext == "" {
				continue
			}
			if !containsFold(byExtension[ext], relation) {
				byExtension[ext] = append(byExtension[ext], relation)
			}
		}
	}

	suggestions := []RegistrationSuggestion{}
	seen := map[string]bool{}
	for _, link := range closure.Edges {
		if !members[link.From] || !members[link.To] {
			continue
		}
		if !link.Ref.Followable() {
			continue
		}
		entryPath := closure.PathOf(link.To)
		if entryPath == "" {
			continue
		}
		normalized := normalizeRegistryPath(entryPath)
		ext := strings.ToLower(path.Ext(normalized))
		relations := byExtension[ext]
		if len(relations) == 0 {
			continue
		}
		alreadyRegistered := false
		for _, relation := range relations {
			if _, ok := pathIndexes[relation][normalized]; ok {
				alreadyRegistered = true
				break
			}
		}
		if alreadyRegistered {
			continue
		}
		// One suggestion per (relation target set, entry): several relations may
		// share an extension, and each one is a separate place to register.
		for _, relation := range relations {
			key := relation + "|" + normalized
			if seen[key] {
				continue
			}
			seen[key] = true
			suggestions = append(suggestions, RegistrationSuggestion{
				Relation:     relation,
				ListPath:     listPaths[relation],
				EntryPath:    entryPath,
				ReferencedBy: closure.PathOf(link.From),
				EdgeID:       link.Ref.EdgeID,
			})
		}
	}
	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].Relation != suggestions[j].Relation {
			return suggestions[i].Relation < suggestions[j].Relation
		}
		return suggestions[i].EntryPath < suggestions[j].EntryPath
	})
	return suggestions, nil
}
