package services

import (
	"fmt"

	"pvfine/internal/importing"
	"pvfine/internal/pvf"
)

// ImportListCounts summarises the registry half of a plan.
type ImportListCounts struct {
	Total      int `json:"total"`
	New        int `json:"new"`
	Registered int `json:"registered"`
	IDTaken    int `json:"idTaken"`
	Duplicate  int `json:"duplicate"`
	Dangling   int `json:"dangling"`
	NoTable    int `json:"noTable"`
	Selected   int `json:"selected"`
}

// ImportListEntry is one registration row in the preview.
type ImportListEntry struct {
	Key        string `json:"key"`
	ListPath   string `json:"listPath"`
	ID         string `json:"id"`
	TargetPath string `json:"targetPath"`
	State      string `json:"state"`
	Resolution string `json:"resolution"`
	Selected   bool   `json:"selected"`
	ConflictOf string `json:"conflictOf"`
	Reason     string `json:"reason"`
}

type ImportListPage struct {
	Entries []ImportListEntry `json:"entries"`
	Cursor  int               `json:"cursor"`
	Next    int               `json:"next"`
	Total   int               `json:"total"`
}

// ListPage returns one slice of the registration plan.
func (s *ImportCenterService) ListPage(id string, cursor, limit int) (*ImportListPage, error) {
	s.mu.Lock()
	plan := s.plan
	s.mu.Unlock()
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	if limit <= 0 || limit > importCenterPageLimit {
		limit = importCenterPageLimit
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(plan.listEntries) {
		cursor = len(plan.listEntries)
	}
	end := cursor + limit
	if end > len(plan.listEntries) {
		end = len(plan.listEntries)
	}
	page := &ImportListPage{Total: len(plan.listEntries), Cursor: cursor, Next: end}
	if end >= len(plan.listEntries) {
		page.Next = -1
	}
	for offset := cursor; offset < end; offset++ {
		page.Entries = append(page.Entries, importListEntryView(fmt.Sprint(offset), plan.listEntries[offset]))
	}
	return page, nil
}

// SetListDecision changes one row's write flag and, for conflicts, how the
// conflict is resolved. Only the three choices the engine understands are
// accepted: skip / overwrite / remove-old.
func (s *ImportCenterService) SetListDecision(id, key string, selected bool, resolution string) (*ImportCenterPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plan
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	index, err := parseImportListKey(key)
	if err != nil || index < 0 || index >= len(plan.listEntries) {
		return nil, fmt.Errorf("登记行不存在: %s", key)
	}
	entry := &plan.listEntries[index]
	if !entry.Selected && selected {
		if entry.State != importing.ListNew && entry.State != importing.ListIDTaken &&
			entry.State != importing.ListDuplicate {
			return nil, fmt.Errorf("该行不可写入（%s）", entry.State)
		}
	}
	switch importing.ListResolution(resolution) {
	case importing.ResolveSkip, importing.ResolveOverwrite, importing.ResolveRemoveOld:
		entry.Resolution = importing.ListResolution(resolution)
	case "":
		// 保持原值
	default:
		return nil, fmt.Errorf("未知的消解方式: %s", resolution)
	}
	entry.Selected = selected
	if entry.State == importing.ListNew && entry.Resolution == "" {
		entry.Resolution = importing.ResolveSkip
	}
	preview := plan.summary()
	return &preview, nil
}

func importListEntryView(key string, entry importing.ListEntry) ImportListEntry {
	return ImportListEntry{
		Key:        key,
		ListPath:   entry.ListPath,
		ID:         entry.ID,
		TargetPath: entry.TargetPath,
		State:      string(entry.State),
		Resolution: string(entry.Resolution),
		Selected:   entry.Selected,
		ConflictOf: entry.ConflictOf,
		Reason:     entry.Reason,
	}
}

func parseImportListKey(key string) (int, error) {
	var index int
	if _, err := fmt.Sscanf(key, "%d", &index); err != nil {
		return -1, err
	}
	// Sscanf 会把 "3abc" 读成 3，这里要求整串都是数字。
	if fmt.Sprint(index) != key {
		return -1, fmt.Errorf("非法键: %s", key)
	}
	return index, nil
}

func (p *importCenterPlan) listCounts() ImportListCounts {
	counts := ImportListCounts{Total: len(p.listEntries)}
	for _, entry := range p.listEntries {
		switch entry.State {
		case importing.ListNew:
			counts.New++
		case importing.ListRegistered:
			counts.Registered++
		case importing.ListIDTaken:
			counts.IDTaken++
		case importing.ListDuplicate:
			counts.Duplicate++
		case importing.ListDangling:
			counts.Dangling++
		case importing.ListNoTable:
			counts.NoTable++
		}
		if entry.Selected {
			counts.Selected++
		}
	}
	return counts
}

// writesListPairs reports whether any selected row would add or repoint a pair
// (as opposed to only removing a duplicate). Adding a row appends to the string
// pool, which is a structural change a hash-locked archive cannot save.
func (p *importCenterPlan) writesListPairs() bool {
	for _, entry := range p.listEntries {
		if !entry.Selected || entry.ListIndex < 0 {
			continue
		}
		switch entry.State {
		case importing.ListNew:
			return true
		case importing.ListIDTaken:
			if entry.Resolution == importing.ResolveOverwrite {
				return true
			}
		case importing.ListDuplicate:
			if entry.Resolution == importing.ResolveRemoveOld {
				return true
			}
		}
	}
	return false
}

// changedListPaths predicts which tables ApplyListRows will touch, including the
// `_indexhash.etc` companion. Execute needs this *before* it applies anything:
// the version snapshot is taken up front, and a table missing from it would be
// left modified after an undo.
func (p *importCenterPlan) changedListPaths() []string {
	seen := make(map[string]struct{}, len(p.listEntries))
	result := make([]string, 0, len(p.listEntries))
	add := func(path string) {
		if path == "" {
			return
		}
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	for _, entry := range p.listEntries {
		if !entry.Selected || entry.ListIndex < 0 {
			continue
		}
		writes := false
		switch entry.State {
		case importing.ListNew:
			writes = true
		case importing.ListIDTaken:
			writes = entry.Resolution == importing.ResolveOverwrite
		case importing.ListDuplicate:
			writes = entry.Resolution == importing.ResolveRemoveOld
		}
		if !writes {
			continue
		}
		add(entry.ListPath)
		if p.requiresIndexHash {
			if hashPath, ok := pvf.IndexHashCompanionPath(entry.ListPath); ok {
				add(hashPath)
			}
		}
	}
	return result
}
