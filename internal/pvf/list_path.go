package pvf

import (
	"path"
	"strings"
)

// ResolveListPath turns a `.lst` entry's path as written in the table into an
// archive-root-relative path, assuming the 90US layout (entry paths are
// relative to the list's own directory).
func ResolveListPath(listPath, relative string) (string, bool) {
	relative = strings.TrimSpace(strings.ReplaceAll(relative, "\\", "/"))
	if relative == "" || strings.HasPrefix(relative, "/") {
		return "", false
	}
	resolved := path.Clean(path.Join(path.Dir(listPath), relative))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", false
	}
	return resolved, true
}

// FindListTarget resolves a `.lst` entry to the archive entry it denotes.
//
// This lives in the kernel rather than in services because three callers need
// exactly the same 90/110 interpretation: registration, export dependencies and
// the import centre. A second copy is how one of them drifts.
func FindListTarget(a *Archive, listPath, relative string) (string, int32, bool) {
	if a == nil {
		return "", 0, false
	}
	candidates, ok := ListPathCandidates(listPath, relative)
	if !ok {
		return "", 0, false
	}
	for _, candidate := range candidates {
		if index, found := a.Find(candidate); found {
			return a.Path(index), index, true
		}
	}
	return "", 0, false
}

// ListPathCandidates returns the archive paths a `.lst` entry may denote. The
// 90US layout stores entry paths relative to the list's own directory
// (`equipment/equipment.lst` + `character/a.equ`), while the 110US layout
// stores archive-root-relative paths (`list/equipment.lst` +
// `equipment/character/a.equ`). Both readings are tried, each also with the
// `(r)` override file name the client falls back to.
func ListPathCandidates(listPath, relative string) ([]string, bool) {
	targetPath, ok := ResolveListPath(listPath, relative)
	if !ok {
		return nil, false
	}
	candidates := []string{targetPath}
	appendOverride := func(candidate string) {
		base := path.Base(candidate)
		if strings.HasPrefix(strings.ToLower(base), "(r)") {
			return
		}
		candidates = append(candidates, path.Join(path.Dir(candidate), "(r)"+base))
	}
	appendOverride(targetPath)
	root := strings.Trim(strings.ReplaceAll(strings.TrimSpace(relative), "\\", "/"), "/")
	if root != "" && !strings.Contains(root, "..") {
		if cleaned := path.Clean(root); cleaned != "." && cleaned != targetPath {
			candidates = append(candidates, cleaned)
			appendOverride(cleaned)
		}
	}
	return candidates, true
}

// ListEntryPath returns how a file path is written inside a given `.lst`.
//
// The 90US tables store paths relative to the table's own directory, while the
// paged110 layout keeps archive-root-relative paths (`list/...`). Writing a row
// with the wrong form produces a line the client cannot resolve, so the form is
// always recomputed for the target table rather than copied from the source.
func ListEntryPath(listPath, filePath string) string {
	listPath = strings.Trim(strings.ReplaceAll(listPath, "\\", "/"), "/")
	filePath = strings.Trim(strings.ReplaceAll(filePath, "\\", "/"), "/")
	dir := path.Dir(listPath)
	if dir != "." && !strings.EqualFold(dir, "list") {
		prefix := dir + "/"
		if len(filePath) >= len(prefix) && strings.EqualFold(filePath[:len(prefix)], prefix) {
			return filePath[len(prefix):]
		}
	}
	return filePath
}
