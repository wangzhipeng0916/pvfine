package services

import (
	"fmt"
	"path"
	"strings"
	"time"

	"pvfine/internal/extraction"
	modpkg "pvfine/internal/mod"
	"pvfine/internal/pvf"
)

func (s *exportSource) findList(configured string) (string, bool) {
	if s.snapshot == nil {
		i, ok := s.archive.FindList(configured)
		if ok {
			return s.archive.Path(i), true
		}
		return "", false
	}
	candidates := []string{configured, "list/" + path.Base(configured)}
	if strings.EqualFold(path.Dir(configured), "list") {
		candidates = append(candidates, strings.TrimSuffix(path.Base(configured), ".lst")+"/"+path.Base(configured))
	}
	for _, p := range candidates {
		if actual, ok := s.lookup(p); ok {
			return actual, true
		}
	}
	return "", false
}

func (s *exportSource) listTarget(listPath, relative string) (string, bool) {
	candidates, ok := listPathCandidates(listPath, relative)
	if !ok {
		return "", false
	}
	for _, candidate := range candidates {
		if p, ok := s.lookup(candidate); ok {
			return p, true
		}
	}
	return "", false
}

type exportTableMapping struct {
	listPath string
	pair     modpkg.Pair
}

func (s *ExportService) addExportDependenciesLocked(source *exportSource, pkg *modpkg.Package, writer modpkg.Writer) (int, error) {
	explicit := make(map[string]int, len(pkg.Entries))
	scripts := []modpkg.Entry{}
	targets := map[string]bool{}
	for i, entry := range pkg.Entries {
		explicit[strings.ToLower(entry.Path)] = i
		if entry.Operation == modpkg.ReplaceFile {
			targets[strings.ToLower(entry.Path)] = true
			if entry.DataType == pvf.TypeScript {
				scripts = append(scripts, entry)
			}
		}
	}
	if len(targets) == 0 {
		return 0, nil
	}
	type scriptReference struct{ path, value string }
	references := []scriptReference{}
	seenRefs := map[string]bool{}
	for _, script := range scripts {
		if !strings.Contains(string(script.Data), "<") {
			continue
		}
		for _, element := range pvf.ParseScriptView(string(script.Data)).Elements {
			if element.TokenType != 8 && element.TokenType != 10 {
				continue
			}
			for _, reference := range exportPlaceholders(element.Value) {
				if !seenRefs[reference] {
					references = append(references, scriptReference{script.Path, reference})
					seenRefs[reference] = true
				}
			}
		}
	}
	count := 0
	// Explicit contents win; dependency additions only fill absent keys.
	add := func(p string, operation modpkg.Operation, pair modpkg.Pair) {
		key := strings.ToLower(p)
		if i, ok := explicit[key]; ok {
			entry := &pkg.Entries[i]
			if entry.Operation == modpkg.ReplaceFile {
				return
			}
			for _, existing := range entry.Pairs {
				if existing.Key == pair.Key {
					return
				}
			}
		}
		entry := modpkg.Entry{Path: p, Operation: operation, Encoding: modpkg.Text, Pairs: []modpkg.Pair{pair}}
		if err := writer.Validate(modpkg.Package{Metadata: pkg.Metadata, Entries: []modpkg.Entry{entry}}); err != nil {
			source.diagnostics.skip("无法导出依赖条目 %s / %s: %v", p, pair.Key, err)
			return
		}
		if i, ok := explicit[key]; ok {
			pkg.Entries[i].Pairs = append(pkg.Entries[i].Pairs, pair)
		} else {
			explicit[key] = len(pkg.Entries)
			pkg.Entries = append(pkg.Entries, entry)
		}
		count++
	}
	cache := map[string][]modpkg.Pair{}
	stringValues := map[string]map[string]string{}
	readPairs := func(p string, operation modpkg.Operation) []modpkg.Pair {
		key := strings.ToLower(p)
		if pairs, ok := cache[key]; ok {
			return pairs
		}
		started := time.Now()
		content, err := source.read(p)
		if err != nil {
			source.diagnostics.skip("无法读取依赖表 %s: %v", p, err)
			cache[key] = nil
			return nil
		}
		readElapsed := time.Since(started)
		pairs, err := parseExportPairsReported(*content, operation, &source.diagnostics)
		if err != nil {
			source.diagnostics.skip("无法解析依赖表 %s: %v", p, err)
			cache[key] = nil
			return nil
		}
		developmentLog("[pvfine:export] dependency table=%s rows=%d read=%s parse=%s",
			p, len(pairs), readElapsed.Round(time.Millisecond), (time.Since(started) - readElapsed).Round(time.Millisecond))
		cache[key] = pairs
		return pairs
	}
	mappings := map[string][]exportTableMapping{}
	for _, list := range []string{
		"list/n_string.lst", "n_string.lst",
		"list/n_string_translate.lst", "n_string_translate.lst",
		"list/n_string_kor.lst", "n_string_kor.lst",
	} {
		if len(references) == 0 {
			break
		}
		actual, ok := source.lookup(list)
		if !ok {
			continue
		}
		pairs := readPairs(actual, modpkg.MergeList)
		for _, pair := range pairs {
			mappings[pair.Key] = append(mappings[pair.Key], exportTableMapping{actual, pair})
		}
	}
	for _, ref := range references {
		reference := ref.value
		table, key, _ := pvf.ParsePlaceholder(reference)
		candidates := mappings[fmt.Sprint(table)]
		found, emptyFound := false, false
		var emptyMapping exportTableMapping
		var emptyPath string
		for _, mapping := range candidates {
			strPath, exists := source.listTarget(mapping.listPath, mapping.pair.Value)
			if !exists {
				continue
			}
			pairs := readPairs(strPath, modpkg.MergeStrings)
			values, cached := stringValues[strPath]
			if !cached {
				values = pairMap(pairs)
				stringValues[strPath] = values
			}
			value, exists := values[key]
			if !exists {
				continue
			}
			if value == "" {
				if !emptyFound {
					emptyMapping, emptyPath, emptyFound = mapping, strPath, true
				}
				continue
			}
			add(strPath, modpkg.MergeStrings, modpkg.Pair{Key: key, Value: value})
			add(mapping.listPath, modpkg.MergeList, mapping.pair)
			found = true
			break
		}
		if !found && emptyFound {
			add(emptyPath, modpkg.MergeStrings, modpkg.Pair{Key: key, Value: ""})
			add(emptyMapping.listPath, modpkg.MergeList, emptyMapping.pair)
			found = true
		}
		if !found {
			source.diagnostics.skip("文字表依赖缺失或无法解析: %s 引用 %s", ref.path, reference)
		}
	}
	seenLists, registered := map[string]bool{}, map[string]bool{}
	for _, spec := range s.c.searchableListSpecsLocked() {
		list, ok := source.findList(spec.listPath)
		if !ok || seenLists[strings.ToLower(list)] {
			continue
		}
		seenLists[strings.ToLower(list)] = true
		pairs := readPairs(list, modpkg.MergeList)
		for _, pair := range pairs {
			// Most registration rows do not target any exported file. Avoid
			// archive lookups for those rows, but preserve candidate precedence
			// when a row could match an exported path.
			candidates, valid := listPathCandidates(list, pair.Value)
			if !valid {
				continue
			}
			matches := false
			for _, candidate := range candidates {
				if targets[strings.ToLower(candidate)] {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
			target, ok := source.listTarget(list, pair.Value)
			if ok && targets[strings.ToLower(target)] {
				add(list, modpkg.MergeList, pair)
				registered[strings.ToLower(target)] = true
			}
		}
	}
	for target := range targets {
		ext := strings.ToLower(path.Ext(target))
		if (ext == ".equ" || ext == ".stk") && !registered[target] {
			source.diagnostics.skip("物品列表依赖缺失: %s 未在配置的列表中登记", target)
		}
	}
	return count, nil
}

func exportPlaceholders(value string) []string {
	result := []string{}
	for {
		start := strings.IndexByte(value, '<')
		if start < 0 {
			break
		}
		value = value[start:]
		end := strings.IndexByte(value, '>')
		if end < 0 {
			break
		}
		candidate := value[:end+1]
		if _, _, ok := pvf.ParsePlaceholder(candidate); ok {
			result = append(result, candidate)
		}
		value = value[end+1:]
	}
	return result
}

// exportClosureAdditions caps how many entries reference-closure completion may
// add to one package. Each addition carries whole-file content in memory, so an
// uncapped walk over a whole-archive change set could ask for the entire game.
// The limit is deliberately low enough to stay safe and high enough that a real
// content MOD (a dungeon pulls ~30k entries *total*, most already present) fits;
// hitting it is reported, never silent.
const exportClosureAdditions = 20000

// addExportReferenceClosureLocked completes a MOD package with the entries its
// own included files reference, transitively. This is the part
// addExportDependenciesLocked cannot do by design: that pass knows text tables,
// table-number mappings and list rows, while scripts, animations, particles and
// sound references were never covered (docs/MOD.md says so out loud).
//
// It returns the number added plus notes for the human-readable preview:
// dangling references the archive itself cannot satisfy, and any cap it hit.
func (s *ExportService) addExportReferenceClosureLocked(source *exportSource, pkg *modpkg.Package,
	writer modpkg.Writer) (int, []string, error) {
	if source.snapshot != nil {
		// The scanner resolves inside an Archive; a commit export reads content
		// out of a snapshot, so walking the live archive would complete the package
		// against files the exported commit never contained.
		return 0, []string{"导出的是历史提交，未做引用闭包补齐（闭包只能按当前归档解析）"}, nil
	}
	seeds := []int32{}
	included := make(map[string]bool, len(pkg.Entries))
	for _, entry := range pkg.Entries {
		key := strings.ToLower(entry.Path)
		included[key] = true
		// Merge entries are table deltas, not whole files: their references are
		// already handled by the table pass, and scanning them as scripts would
		// resolve against content the package does not carry.
		if entry.Operation != modpkg.ReplaceFile {
			continue
		}
		if index, ok := source.archive.Find(entry.Path); ok {
			seeds = append(seeds, index)
		}
	}
	if len(seeds) == 0 {
		return 0, nil, nil
	}
	// Prepare holds the core read lock for its whole body, so the cache identity is
	// read directly and the witness is taken without re-locking: a recursive
	// RLock deadlocks against a waiting writer.
	scanner, err := s.scanners.forArchive(s.c, source.archive, s.c.archiveGeneration, s.c.annotationEngine,
		unlockedWitness(), nil)
	if err != nil {
		return 0, nil, err
	}
	closure, err := scanner.Closure(seeds, extraction.Options{MaxEntries: exportClosureAdditions})
	if err != nil {
		return 0, nil, err
	}
	added := 0
	for _, node := range closure.Nodes {
		key := strings.ToLower(node.Path)
		if included[key] {
			continue
		}
		content, err := source.read(node.Path)
		if err != nil {
			source.diagnostics.skip("无法读取引用依赖 %s: %v", node.Path, err)
			continue
		}
		entry, _, err := exportModEntryReported(*content, nil, &source.diagnostics)
		if err != nil {
			source.diagnostics.skip("无法生成引用依赖条目 %s: %v", node.Path, err)
			continue
		}
		if entry == nil {
			continue
		}
		if err := writer.Validate(modpkg.Package{Metadata: pkg.Metadata, Entries: []modpkg.Entry{*entry}}); err != nil {
			source.diagnostics.skip("引用依赖 %s 不被当前格式接受: %v", node.Path, err)
			continue
		}
		included[key] = true
		pkg.Entries = append(pkg.Entries, *entry)
		added++
	}
	notes := []string{}
	if closure.Truncated {
		notes = append(notes, fmt.Sprintf("引用闭包在 %d 条上限处停止，还有 %d 个可达条目未展开；完整清单请用提取中心的 MOD 计划",
			exportClosureAdditions, closure.Dropped))
	}
	// 归档里本来就不存在的目标不是补齐能解决的，但必须说出来：用户看到的就是
	// "我的包发出去会不会引用到没有的文件"。逐条目重扫而不是读 closure.Unresolved，
	// 因为后者不带"这条引用来自哪个文件"，而报表要能指名道姓。扫描结果有缓存，
	// 这一步不重新解析文本。
	dangling := 0
	examples := []string{}
	seen := map[string]bool{}
	for _, node := range closure.Nodes {
		result, err := scanner.ScanFile(node.Index)
		if err != nil {
			continue
		}
		for _, ref := range result.Refs {
			target := strings.TrimSpace(ref.TargetPath)
			if target == "" || ref.Kind == extraction.TargetAsset || ref.State == extraction.StateResolved {
				continue
			}
			if _, ok := source.archive.Find(target); ok {
				continue
			}
			key := strings.ToLower(target)
			if seen[key] {
				continue
			}
			seen[key] = true
			dangling++
			if len(examples) < 3 {
				examples = append(examples, fmt.Sprintf("%s [%s] → %s",
					path.Base(node.Path), ref.Section, target))
			}
		}
	}
	if dangling > 0 {
		notes = append(notes, fmt.Sprintf("包内引用有 %d 个目标在本归档里不存在（例：%s）", dangling, strings.Join(examples, "；")))
	}
	return added, notes, nil
}
