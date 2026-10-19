package extraction

import (
	"context"
	"path"
	"sort"
	"strings"
)

// OrphanBucket names why an entry survived the "nothing references it" filter.
// Reporting orphans without this split sells a quarter of an archive as dead
// content, which the measurement below says is wrong.
type OrphanBucket string

const (
	// OrphanUndeclaredEdge is reachable from a path-shaped candidate that no edge
	// claims yet: declaring that class removes the orphan. This is #23's backlog
	// counted against actual content.
	OrphanUndeclaredEdge OrphanBucket = "未声明边的受害者"
	// OrphanFailedRef is named by a reference that did not resolve (missing or
	// ambiguous): somebody asks for it, so the gap is archive content or a version
	// difference, not necessarily dead content.
	OrphanFailedRef OrphanBucket = "被提到但解析失败"
	// OrphanTemplateNamed is named only by a runtime-substituted path (a value
	// containing %s/%d/*), which static analysis can never resolve. It is counted
	// apart so the remaining bucket is not inflated by content the game does load.
	OrphanTemplateNamed OrphanBucket = "动态模板可解释"
	// OrphanUnexplained is what is left: no declared edge, no candidate, no failed
	// reference, no template, no registry row. Only this bucket may be called
	// unreferenced.
	OrphanUnexplained OrphanBucket = "无人引用"
)

// OrphanAttribution splits the orphan set by cause.
type OrphanAttribution struct {
	Buckets     map[OrphanBucket]int
	ByExtension map[string]map[OrphanBucket]int
	Total       int
	// AssetOnly counts entries that only client-asset edges name: live content
	// that is outside every text closure, reported next to the orphan split so the
	// two are never conflated.
	AssetOnly int

	orphanSet       map[int32]bool
	undeclaredEdges map[int32]bool
	failedRefs      map[int32]bool
	templateNamed   map[int32]bool
	// Templates counts the usable runtime-substitution patterns, and Degenerate
	// the ones dropped because they carry no literal: a bare `%s` matches
	// everything, which would make the attribution self-proving and worthless.
	Templates  int
	Degenerate int
	// Comparisons records how many orphan/pattern pairs the matcher actually
	// tested, so the cost of this bucket is visible rather than assumed.
	Comparisons int
}

// AttributeOrphans explains the orphan set. It rescans the universe because the
// evidence it needs — unclaimed path candidates and references that failed to
// resolve — lives in per-entry scan results, not in the reverse index.
func AttributeOrphans(ctx context.Context, scanner *Scanner, reverse *ReverseIndex, workers int) (*OrphanAttribution, error) {
	orphans, _ := reverse.Orphans()
	attribution := &OrphanAttribution{
		Buckets:         map[OrphanBucket]int{},
		ByExtension:     map[string]map[OrphanBucket]int{},
		Total:           len(orphans),
		AssetOnly:       len(reverse.AssetOnly()),
		orphanSet:       make(map[int32]bool, len(orphans)),
		undeclaredEdges: map[int32]bool{},
		failedRefs:      map[int32]bool{},
		templateNamed:   map[int32]bool{},
	}
	patterns := map[string]bool{}
	byPath := make(map[string]int32, len(orphans))
	for _, index := range orphans {
		attribution.orphanSet[index] = true
		byPath[normalizeRegistryPath(scanner.archive.FullPath(index))] = index
	}

	_, err := scanner.ScanIndices(ctx, reverse.Indexes(), workers, func(result ScanResult) {
		for _, candidate := range result.Unmodeled {
			if candidate.Shape != "path" || candidate.Value == "" {
				continue
			}
			if isDynamicPath(candidate.Value) {
				for _, pattern := range dynamicPatterns(result.Path, candidate.Value) {
					patterns[pattern] = true
				}
				continue
			}
			target, ok := scanner.resolveCandidate(result.Path, candidate.Value)
			if ok && attribution.orphanSet[target] {
				attribution.undeclaredEdges[target] = true
			}
		}
		for _, ref := range result.Refs {
			if ref.State == StateResolved {
				continue
			}
			if ref.TargetPath != "" {
				if index, ok := byPath[normalizeRegistryPath(ref.TargetPath)]; ok {
					attribution.failedRefs[index] = true
				}
			}
			if strings.Contains(ref.Note, "动态路径") {
				for _, pattern := range dynamicPatterns(result.Path, ref.Value) {
					patterns[pattern] = true
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}

	// 动态模板档：静态判不了不等于没人要。字面量为空的模板（`%s`）必须剔除，
	// 否则"全部孤儿都能被模板解释"是自证结论——这条是靠实测学到的。
	usable := make([]string, 0, len(patterns))
	for pattern := range patterns {
		if len(literalRunes(pattern)) < 3 {
			attribution.Degenerate++
			continue
		}
		usable = append(usable, pattern)
	}
	attribution.Templates = len(usable)
	attribution.templateNamed = matchTemplates(usable, scanner, attribution.orphanSet, attribution)

	for _, index := range orphans {
		bucket := OrphanUnexplained
		switch {
		case attribution.undeclaredEdges[index]:
			bucket = OrphanUndeclaredEdge
		case attribution.templateNamed[index]:
			bucket = OrphanTemplateNamed
		case attribution.failedRefs[index]:
			bucket = OrphanFailedRef
		}
		attribution.Buckets[bucket]++
		ext := strings.ToLower(path.Ext(scanner.archive.FullPath(index)))
		if attribution.ByExtension[ext] == nil {
			attribution.ByExtension[ext] = map[OrphanBucket]int{}
		}
		attribution.ByExtension[ext][bucket]++
	}
	return attribution, nil
}

// matchTemplates marks the orphans some runtime-substitution pattern can name.
// Patterns are grouped by extension and matched with a literal-prefix scan over
// the sorted orphan list of that extension, so the work stays proportional to
// the prefixes rather than to the full cross product.
func matchTemplates(patterns []string, scanner *Scanner, orphans map[int32]bool, attribution *OrphanAttribution) map[int32]bool {
	marked := map[int32]bool{}
	byExt := map[string][]string{}
	for pattern := range patternsMap(patterns) {
		byExt[strings.ToLower(path.Ext(pattern))] = append(byExt[strings.ToLower(path.Ext(pattern))], pattern)
	}
	sorted := make([]int32, 0, len(orphans))
	for index := range orphans {
		sorted = append(sorted, index)
	}
	// 排序键必须与二分查找用的键完全一致（同为小写归一化路径），否则 sort.Search
	// 落在无关位置，整段匹配会静默全零。
	orphanPaths := make([]string, len(sorted))
	for at, index := range sorted {
		orphanPaths[at] = normalizeRegistryPath(scanner.archive.FullPath(index))
	}
	sort.Sort(byOrphanPath{paths: orphanPaths, indexes: sorted})
	for _, list := range byExt {
		sort.Strings(list)
		for _, pattern := range list {
			prefix, _ := literalBounds(pattern)
			low := 0
			if prefix != "" {
				low = sort.Search(len(orphanPaths), func(i int) bool {
					return orphanPaths[i] >= prefix
				})
			}
			for at := low; at < len(orphanPaths); at++ {
				candidate := orphanPaths[at]
				if prefix != "" && !strings.HasPrefix(candidate, prefix) {
					break
				}
				attribution.Comparisons++
				if !globEqual(pattern, candidate) {
					continue
				}
				marked[sorted[at]] = true
			}
		}
	}
	return marked
}

func patternsMap(patterns []string) map[string]bool {
	out := make(map[string]bool, len(patterns))
	for _, pattern := range patterns {
		out[pattern] = true
	}
	return out
}

// dynamicPatterns turns a runtime-substituted value into the absolute forms the
// archive itself would try: relative to the referencing entry, then from the
// archive root. Without this a value like `templat%s.ani` can never match the
// orphan path `passiveobject/obj/templated.ani`, because the segment counts
// differ by the directory.
func dynamicPatterns(fromPath, value string) []string {
	value = normalizeRegistryPath(value)
	if value == "" {
		return nil
	}
	out := []string{value}
	if dir := normalizeRegistryPath(path.Dir(fromPath)); dir != "" && dir != "." {
		out = append(out, normalizeRegistryPath(path.Join(dir, value)))
	}
	return out
}

// literalBounds splits a template at its first and last wildcard spec. A spec is
// `%` plus its conversion letters, or a single `*`/`?`: cutting at the `%` alone
// would leave the trailing `s` of `%s` in the "literal" suffix and match nothing.
func literalBounds(pattern string) (prefix, suffix string) {
	spans := wildcardSpans(pattern)
	if len(spans) == 0 {
		return pattern, pattern
	}
	prefix = pattern[:spans[0][0]]
	last := spans[len(spans)-1]
	if last[1] >= len(pattern) {
		return prefix, ""
	}
	return prefix, pattern[last[1]:]
}

// wildcardSpans lists [start, end) byte ranges that a runtime value substitutes.
func wildcardSpans(pattern string) [][2]int {
	out := [][2]int{}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*', '?':
			end := i + 1
			for end < len(pattern) && (pattern[end] == '*' || pattern[end] == '?') {
				end++
			}
			out = append(out, [2]int{i, end})
			i = end - 1
		case '%':
			// A conversion is flags/width, then exactly one letter: consuming
			// greedily would swallow the `.ani` that follows `%s` and turn the
			// literal suffix into nothing.
			end := i + 1
			for end < len(pattern) && (pattern[end] == '-' || pattern[end] == '.' ||
				pattern[end] == '0' || pattern[end] == '1' || pattern[end] == '2' ||
				pattern[end] == '3' || pattern[end] == '4' || pattern[end] == '5' ||
				pattern[end] == '6' || pattern[end] == '7' || pattern[end] == '8' ||
				pattern[end] == '9') {
				end++
			}
			if end >= len(pattern) || !isASCIILetter(pattern[end]) {
				continue
			}
			end++
			out = append(out, [2]int{i, end})
			i = end - 1
		}
	}
	return out
}

func isASCIILetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

type byOrphanPath struct {
	paths   []string
	indexes []int32
}

func (b byOrphanPath) Len() int { return len(b.paths) }
func (b byOrphanPath) Swap(i, j int) {
	b.paths[i], b.paths[j] = b.paths[j], b.paths[i]
	b.indexes[i], b.indexes[j] = b.indexes[j], b.indexes[i]
}
func (b byOrphanPath) Less(i, j int) bool { return b.paths[i] < b.paths[j] }

// globEqual compares segment by segment, treating a wildcard run as any single
// segment. It is deliberately strict: `%s` standing for a whole path would
// over-claim, and an over-claiming attribution is worse than none.
func globEqual(pattern, value string) bool {
	patternParts := strings.Split(pattern, "/")
	valueParts := strings.Split(value, "/")
	if len(patternParts) != len(valueParts) {
		return false
	}
	for at := range patternParts {
		if strings.ContainsAny(patternParts[at], "%*?") {
			prefix, suffix := literalBounds(patternParts[at])
			if len(prefix)+len(suffix) > len(valueParts[at]) {
				return false
			}
			if !strings.HasPrefix(valueParts[at], prefix) ||
				(suffix != "" && !strings.HasSuffix(valueParts[at], suffix)) {
				return false
			}
			continue
		}
		if patternParts[at] != valueParts[at] {
			return false
		}
	}
	return true
}

// literalRunes returns the characters a template pins down, ignoring separators
// and punctuation that carry no identity.
func literalRunes(pattern string) string {
	out := make([]rune, 0, len(pattern))
	for i := 0; i < len(pattern); i++ {
		char := pattern[i]
		if char == '%' {
			for i+1 < len(pattern) && (pattern[i+1] == 'd' || pattern[i+1] == 's' ||
				pattern[i+1] == '.' || (pattern[i+1] >= '0' && pattern[i+1] <= '9')) {
				i++
			}
			continue
		}
		if char == '*' || char == '?' || char == '/' || char == '_' || char == '-' || char == '.' {
			continue
		}
		out = append(out, rune(char))
	}
	return string(out)
}

// OrphanExtRow is one extension's attribution result.
type OrphanExtRow struct {
	Extension      string
	UndeclaredEdge int
	TemplateNamed  int
	FailedRef      int
	Unexplained    int
}

// Total sums the row's three buckets.
func (r OrphanExtRow) Total() int {
	return r.UndeclaredEdge + r.TemplateNamed + r.FailedRef + r.Unexplained
}

// Rows orders the per-extension split so the actionable classes surface first.
func (a *OrphanAttribution) Rows() []OrphanExtRow {
	out := make([]OrphanExtRow, 0, len(a.ByExtension))
	for ext, buckets := range a.ByExtension {
		out = append(out, OrphanExtRow{Extension: ext,
			UndeclaredEdge: buckets[OrphanUndeclaredEdge],
			TemplateNamed:  buckets[OrphanTemplateNamed],
			FailedRef:      buckets[OrphanFailedRef],
			Unexplained:    buckets[OrphanUnexplained]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total() != out[j].Total() {
			return out[i].Total() > out[j].Total()
		}
		return out[i].Extension < out[j].Extension
	})
	return out
}

// Unexplained reports whether one entry is in the only bucket that may be called
// unreferenced content.
func (a *OrphanAttribution) Unexplained(index int32) bool {
	return a.orphanSet[index] && !a.undeclaredEdges[index] &&
		!a.failedRefs[index] && !a.templateNamed[index]
}

// Samples lists a few unexplained entries per extension, so a human can check
// whether the label is believable before it becomes a product claim.
func (a *OrphanAttribution) Samples(scanner *Scanner, perExt, limit int) map[string][]string {
	out := map[string][]string{}
	full := 0
	for index := int32(0); index < scanner.archive.FileCount() && full < limit; index++ {
		if !a.Unexplained(index) {
			continue
		}
		entryPath := scanner.archive.FullPath(index)
		ext := strings.ToLower(path.Ext(entryPath))
		if len(out[ext]) >= perExt {
			continue
		}
		out[ext] = append(out[ext], entryPath)
		full++
	}
	return out
}
