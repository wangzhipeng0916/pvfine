package extraction

import (
	"sort"
	"strings"
	"sync"

	"pvfine/internal/pvf"
)

// EntryDiff is the path-level verdict between two archives: what only one side
// has, and what both have with different bytes.
type EntryDiff struct {
	OnlyLeft  []string
	OnlyRight []string
	Changed   []string
	// Same counts paths present in both with identical content.
	Same int
	// PoolRenumbered counts the entries whose payload bytes differ but whose
	// content is identical: a TypeScript payload stores strings as string-pool
	// slots, and slot numbers are per-archive. Comparing bytes across two
	// archives would call all of these "changed", which is why content is
	// compared as tokens instead.
	PoolRenumbered int
	// LeftEntries/RightEntries are the totals, so a report can say what share
	// drifted instead of only listing differences.
	LeftEntries  int32
	RightEntries int32
}

// CompareEntries classifies every path by presence and payload equality.
//
// Size is compared before bytes because "same size, different content" is rare
// and cheap to confirm, while a full hash of both archives would cost more than
// the byte compare it replaces. Payload equality is what a MOD author means by
// "this file changed", so nothing here approximates.
func CompareEntries(left, right *pvf.Archive, workers int) (EntryDiff, error) {
	if workers < 1 {
		workers = 1
	}
	const (
		verdictSame = iota
		verdictRenumbered
		verdictChanged
		verdictOnlyLeft
	)
	entries := left.FileCount()
	verdicts := make([]uint8, entries)
	matched := make([]bool, right.FileCount())
	chunks := make([][2]int32, 0, workers)
	step := (entries + int32(workers) - 1) / int32(workers)
	if step < 1 {
		step = 1
	}
	for start := int32(0); start < entries; start += step {
		end := start + step
		if end > entries {
			end = entries
		}
		chunks = append(chunks, [2]int32{start, end})
	}
	var group sync.WaitGroup
	for range chunks {
		group.Add(1)
	}
	for _, chunk := range chunks {
		go func(from, to int32) {
			defer group.Done()
			// 只读访问归档可以并发；Find 与 ScriptViewOf 都是读路径。
			for index := from; index < to; index++ {
				entryPath := left.FullPath(index)
				other, ok := right.Find(entryPath)
				if !ok {
					verdicts[index] = verdictOnlyLeft
					continue
				}
				matched[other] = true
				switch sameContent(left, index, right, other) {
				case contentSame:
					verdicts[index] = verdictSame
				case contentRenumbered:
					verdicts[index] = verdictRenumbered
				default:
					verdicts[index] = verdictChanged
				}
			}
		}(chunk[0], chunk[1])
	}
	group.Wait()

	result := EntryDiff{LeftEntries: entries, RightEntries: right.FileCount()}
	for index := int32(0); index < entries; index++ {
		switch verdicts[index] {
		case verdictSame, verdictRenumbered:
			result.Same++
			if verdicts[index] == verdictRenumbered {
				result.PoolRenumbered++
			}
		case verdictChanged:
			result.Changed = append(result.Changed, left.FullPath(index))
		default:
			result.OnlyLeft = append(result.OnlyLeft, left.FullPath(index))
		}
	}
	for index := int32(0); index < result.RightEntries; index++ {
		if !matched[index] {
			result.OnlyRight = append(result.OnlyRight, right.FullPath(index))
		}
	}
	return result, nil
}

// contentVerdict separates "same content" from "same content, different payload
// bytes", which is what string-pool renumbering between two archives looks like.
type contentVerdict uint8

const (
	contentChanged contentVerdict = iota
	contentSame
	contentRenumbered
)

// sameContent compares content the only way that is comparable across archives:
// a payload stores strings as string-pool slots, and slots are assigned per
// archive, so equal bytes do not imply equal content and different bytes do not
// imply changed content.
func sameContent(left *pvf.Archive, leftIndex int32, right *pvf.Archive, rightIndex int32) contentVerdict {
	if left.File(leftIndex).DataSize != right.File(rightIndex).DataSize {
		return contentChanged
	}
	leftView, leftErr := left.ScriptViewOf(leftIndex)
	rightView, rightErr := right.ScriptViewOf(rightIndex)
	if leftErr != nil || rightErr != nil {
		// 非脚本载荷（TypeUnicode 文本、原始字节）没有池间接，按字节比即可。
		leftBytes, leftErr := left.RawBytes(leftIndex)
		rightBytes, rightErr := right.RawBytes(rightIndex)
		if leftErr != nil || rightErr != nil {
			return contentChanged
		}
		if string(leftBytes) != string(rightBytes) {
			return contentChanged
		}
		return contentSame
	}
	if !sameElements(leftView.Elements, rightView.Elements) {
		return contentChanged
	}
	leftBytes, leftErr := left.RawBytes(leftIndex)
	rightBytes, rightErr := right.RawBytes(rightIndex)
	if leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes) {
		return contentSame
	}
	return contentRenumbered
}

func sameElements(left, right []pvf.ScriptElement) bool {
	if len(left) != len(right) {
		return false
	}
	for at := range left {
		if left[at].TokenType != right[at].TokenType ||
			left[at].Section != right[at].Section ||
			left[at].Value != right[at].Value {
			return false
		}
	}
	return true
}

// RefLink is one reference in a form that means the same thing in two archives.
type RefLink struct {
	EdgeID string
	// Value is the literal token; Target is the resolved archive path, or the
	// location that was tried when it did not resolve.
	Value      string
	Target     string
	State      string
	Section    string
	TargetKind string
}

// key is the identity used to decide "the same reference exists in both".
// State is part of it on purpose: a reference that used to miss and now resolves
// is exactly the kind of drift this comparison exists to surface.
func (r RefLink) key() string {
	return strings.Join([]string{r.EdgeID, r.State, r.TargetKind, r.Value, r.Target}, "\x00")
}

func linkOf(ref Ref) RefLink {
	target := ref.TargetPath
	if ref.Target >= 0 {
		target = normalizeRegistryPath(ref.TargetPath)
	}
	return RefLink{EdgeID: ref.EdgeID, Value: strings.TrimSpace(ref.Value),
		Target: normalizeRegistryPath(target), State: ref.State.String(),
		Section: ref.Section, TargetKind: string(ref.Kind)}
}

// Drift is the reference-set difference for one path that exists in both archives.
type Drift struct {
	Path    string
	Added   []RefLink
	Dropped []RefLink
	// Same counts references identical on both sides, so a row with hundreds of
	// shared links and two changes reads differently from a total reshape.
	Same int
}

// Changed reports whether anything moved.
func (d Drift) Changed() bool { return len(d.Added) > 0 || len(d.Dropped) > 0 }

// CompareReferences scans the same paths in two archives and reports which
// references appeared or disappeared. Both sides must use the same catalog:
// a declaration set is only comparable when it is the same declaration set, and
// "the archive changed, my rules did not" is the question this answers.
func CompareReferences(left, right *Scanner, paths []string, progress func(done, total int)) ([]Drift, error) {
	leftArchive, rightArchive := left.archive, right.archive
	out := make([]Drift, 0, len(paths))
	for at, entryPath := range paths {
		leftIndex, leftOK := leftArchive.Find(entryPath)
		rightIndex, rightOK := rightArchive.Find(entryPath)
		if !leftOK || !rightOK {
			continue
		}
		leftScan, leftErr := left.ScanFile(leftIndex)
		rightScan, rightErr := right.ScanFile(rightIndex)
		if leftErr != nil || rightErr != nil {
			// An entry one side cannot hand back as a script is an entry-level
			// fact, not a reference-level one; CompareEntries already reports it.
			continue
		}
		leftLinks := linkSet(leftScan.Refs)
		rightLinks := linkSet(rightScan.Refs)
		drift := Drift{Path: entryPath}
		for key, link := range rightLinks {
			if _, ok := leftLinks[key]; ok {
				drift.Same++
				continue
			}
			drift.Added = append(drift.Added, link)
		}
		for key, link := range leftLinks {
			if _, ok := rightLinks[key]; !ok {
				drift.Dropped = append(drift.Dropped, link)
			}
		}
		sortLinks(drift.Added)
		sortLinks(drift.Dropped)
		if drift.Changed() {
			out = append(out, drift)
		}
		if progress != nil {
			progress(at+1, len(paths))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func linkSet(refs []Ref) map[string]RefLink {
	out := make(map[string]RefLink, len(refs))
	for _, ref := range refs {
		link := linkOf(ref)
		out[link.key()] = link
	}
	return out
}

func sortLinks(links []RefLink) {
	sort.Slice(links, func(i, j int) bool {
		if links[i].EdgeID != links[j].EdgeID {
			return links[i].EdgeID < links[j].EdgeID
		}
		if links[i].State != links[j].State {
			return links[i].State < links[j].State
		}
		return links[i].Target < links[j].Target
	})
}

// DriftSummary is one catalog edge's share of the reference drift, which is how
// a reviewer finds the section whose columns changed meaning rather than reading
// thousands of individual rows.
type DriftSummary struct {
	EdgeID  string
	Section string
	Added   int
	Dropped int
	// Files counts how many entries show this edge's drift, and Example names one
	// of them so the finding is checkable.
	Files   int
	Example string
}

// SummarizeDrift groups drift by edge.
func SummarizeDrift(drifts []Drift) []DriftSummary {
	type bucket struct {
		section        string
		added, dropped int
		files          map[string]bool
		example        string
	}
	byEdge := map[string]*bucket{}
	for _, drift := range drifts {
		for _, group := range []struct {
			links   []RefLink
			adding  bool
			example *string
		}{{drift.Added, true, nil}, {drift.Dropped, false, nil}} {
			for _, link := range group.links {
				at := byEdge[link.EdgeID]
				if at == nil {
					at = &bucket{section: link.Section, files: map[string]bool{}}
					byEdge[link.EdgeID] = at
				}
				if group.adding {
					at.added++
				} else {
					at.dropped++
				}
				at.files[drift.Path] = true
				if at.example == "" {
					at.example = drift.Path + " → " + link.Target + " (" + link.State + ")"
				}
			}
		}
	}
	out := make([]DriftSummary, 0, len(byEdge))
	for edge, at := range byEdge {
		out = append(out, DriftSummary{EdgeID: edge, Section: at.section,
			Added: at.added, Dropped: at.dropped, Files: len(at.files), Example: at.example})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Added+out[i].Dropped != out[j].Added+out[j].Dropped {
			return out[i].Added+out[i].Dropped > out[j].Added+out[j].Dropped
		}
		return out[i].EdgeID < out[j].EdgeID
	})
	return out
}

// ScopePaths filters a path list to prefixes, so a cross-version run can be
// limited to the directories a reviewer actually cares about.
func ScopePaths(paths []string, prefixes []string) []string {
	if len(prefixes) == 0 {
		return paths
	}
	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		normalized = append(normalized, strings.ToLower(strings.Trim(prefix, "/")))
	}
	out := []string{}
	for _, entryPath := range paths {
		folded := strings.ToLower(entryPath)
		for _, prefix := range normalized {
			if prefix == "" {
				continue
			}
			if strings.HasPrefix(folded, prefix+"/") || folded == prefix ||
				strings.Contains(folded, "/"+prefix+"/") {
				out = append(out, entryPath)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
