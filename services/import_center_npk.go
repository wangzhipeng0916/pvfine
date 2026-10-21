package services

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pvfine/internal/npk"
)

const (
	// 目标目录里的容器数量。超过这个量级说明选错了目录（比如指到了磁盘根）。
	importNPKMaxContainers = 20_000
	importNPKCompareBuf    = 64 * 1024
)

// ImportNPKEntry is one image inside one packaged container, judged against the
// target client directory.
type ImportNPKEntry struct {
	Key       string `json:"key"`
	Container string `json:"container"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	TargetNPK string `json:"targetNpk"`
	// State: identical (同名同字节，可精简) / different (同名不同字节，是覆盖不是重复)
	// / target-missing (目标没有，保留) / unmapped (目标目录不可用)
	State    string `json:"state"`
	Selected bool   `json:"selected"`
}

// ImportNPKCounts summarises the prune plan.
type ImportNPKCounts struct {
	Containers  int   `json:"containers"`
	Entries     int   `json:"entries"`
	Identical   int   `json:"identical"`
	Different   int   `json:"different"`
	Missing     int   `json:"missing"`
	Unmapped    int   `json:"unmapped"`
	Selected    int   `json:"selected"`
	TotalBytes  int64 `json:"totalBytes"`
	PrunedBytes int64 `json:"prunedBytes"`
}

type ImportNPKPreview struct {
	PlanID    string          `json:"planId"`
	TargetDir string          `json:"targetDir"`
	Counts    ImportNPKCounts `json:"counts"`
	Warnings  []string        `json:"warnings"`
}

type ImportNPKPage struct {
	Entries []ImportNPKEntry `json:"entries"`
	Cursor  int              `json:"cursor"`
	Next    int              `json:"next"`
	Total   int              `json:"total"`
}

type ImportNPKResult struct {
	Containers  int    `json:"containers"`
	Written     int    `json:"written"`
	Pruned      int    `json:"pruned"`
	PrunedBytes int64  `json:"prunedBytes"`
	Output      string `json:"output"`
}

// importNPKContainer remembers what the plan saw, so execute can refuse after
// the package changed underneath it (offsets would all be wrong).
type importNPKContainer struct {
	Name    string
	Size    int64
	ModTime time.Time
	Entries []npk.Entry
}

type importNPKTarget struct {
	Path   string
	Offset int64
	Size   int64
}

func (p *importCenterPlan) npkCountsSnapshot() ImportNPKCounts {
	counts := ImportNPKCounts{Containers: len(p.npkContainers), Entries: len(p.npkEntries)}
	for _, entry := range p.npkEntries {
		switch entry.State {
		case "identical":
			counts.Identical++
		case "different":
			counts.Different++
		case "target-missing":
			counts.Missing++
		default:
			counts.Unmapped++
		}
		if entry.Selected {
			counts.Selected++
			counts.PrunedBytes += entry.Size
		}
		counts.TotalBytes += entry.Size
	}
	return counts
}

// PlanNPK compares the packaged containers against the configured client
// directory. It is deliberately on-demand: opening a plan for a text-only
// import must not walk a 3000-container ImagePacks2.
func (s *ImportCenterService) PlanNPK(id string) (*ImportNPKPreview, error) {
	s.mu.Lock()
	plan := s.plan
	s.mu.Unlock()
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}

	targetDir, err := s.importNPKTargetDir()
	if err != nil {
		return nil, err
	}

	containers, entries, warnings, err := planNPK(plan.root, targetDir)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.plan != plan {
		s.mu.Unlock()
		return nil, ErrImportCenterStale
	}
	plan.npkTargetDir = targetDir
	plan.npkContainers = containers
	plan.npkEntries = entries
	plan.npkPlanned = true
	plan.npkWarnings = warnings
	preview := &ImportNPKPreview{
		PlanID: plan.id, TargetDir: targetDir,
		Counts: plan.npkCountsSnapshot(), Warnings: warnings,
	}
	s.mu.Unlock()
	return preview, nil
}

func (s *ImportCenterService) importNPKTargetDir() (string, error) {
	if s.c == nil || s.c.settings == nil {
		return "", fmt.Errorf("没有配置 NPK 目录，无法比对图片")
	}
	settings, err := s.c.settings.GetSettings()
	if err != nil {
		return "", fmt.Errorf("读取设置失败: %w", err)
	}
	if strings.TrimSpace(settings.NPKDirectory) == "" {
		return "", fmt.Errorf("没有配置 NPK 目录，无法比对图片")
	}
	directory, err := canonicalImageDirectory(settings.NPKDirectory)
	if err != nil {
		return "", fmt.Errorf("NPK 目录不可用: %w", err)
	}
	return directory, nil
}

func (s *ImportCenterService) NPKPage(id string, cursor, limit int) (*ImportNPKPage, error) {
	s.mu.Lock()
	plan := s.plan
	s.mu.Unlock()
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	if !plan.npkPlanned {
		return nil, fmt.Errorf("请先比对目标目录")
	}
	if limit <= 0 || limit > importCenterPageLimit {
		limit = importCenterPageLimit
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(plan.npkEntries) {
		cursor = len(plan.npkEntries)
	}
	end := cursor + limit
	if end > len(plan.npkEntries) {
		end = len(plan.npkEntries)
	}
	page := &ImportNPKPage{Total: len(plan.npkEntries), Cursor: cursor, Next: end}
	if end >= len(plan.npkEntries) {
		page.Next = -1
	}
	page.Entries = append(page.Entries, plan.npkEntries[cursor:end]...)
	return page, nil
}

func (s *ImportCenterService) SetNPKSelected(id string, keys []string, selected bool) (*ImportNPKPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plan
	if plan == nil || plan.id != id {
		return nil, ErrImportCenterStale
	}
	if !plan.npkPlanned {
		return nil, fmt.Errorf("请先比对目标目录")
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	for index := range plan.npkEntries {
		entry := &plan.npkEntries[index]
		if _, ok := wanted[entry.Key]; !ok {
			continue
		}
		// 只有"同名同字节"才是可精简的重复；覆盖项不允许被勾选删除。
		if entry.State != "identical" {
			continue
		}
		entry.Selected = selected
	}
	preview := &ImportNPKPreview{
		PlanID: plan.id, TargetDir: plan.npkTargetDir,
		Counts: plan.npkCountsSnapshot(), Warnings: plan.npkWarnings,
	}
	return preview, nil
}

// ExecuteNPK writes pruned copies of the packaged containers into outputDir.
// The target directory is only ever read.
func (s *ImportCenterService) ExecuteNPK(id, outputDir string) (*ImportNPKResult, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil, fmt.Errorf("请选择输出目录")
	}

	s.mu.Lock()
	plan := s.plan
	if plan == nil || plan.id != id {
		s.mu.Unlock()
		return nil, ErrImportCenterStale
	}
	if !plan.npkPlanned {
		s.mu.Unlock()
		return nil, fmt.Errorf("请先比对目标目录")
	}
	containers := plan.npkContainers
	entries := plan.npkEntries
	s.mu.Unlock()

	prune := make(map[string]map[string]struct{}, len(containers))
	report := make([]ImportNPKEntry, 0)
	prunedBytes := int64(0)
	for _, entry := range entries {
		if !entry.Selected {
			continue
		}
		byName := prune[entry.Container]
		if byName == nil {
			byName = make(map[string]struct{})
			prune[entry.Container] = byName
		}
		byName[entry.Name] = struct{}{}
		prunedBytes += entry.Size
		report = append(report, entry)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	result := &ImportNPKResult{Output: outputDir, Pruned: len(report), PrunedBytes: prunedBytes}
	packageDir := filepath.Join(plan.root, "npk")

	for _, container := range containers {
		source := filepath.Join(packageDir, container.Name)
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("读取包内容器 %s 失败: %w", container.Name, err)
		}
		// 计划阶段记录的 offset 只有在源容器没变时才成立。
		if info.Size() != container.Size || !info.ModTime().Equal(container.ModTime) {
			return nil, fmt.Errorf("包内容器 %s 在比对之后被改动，请重新比对", container.Name)
		}

		drop := prune[container.Name]
		keep := make([]npk.Entry, 0, len(container.Entries))
		for _, member := range container.Entries {
			if drop != nil {
				if _, ok := drop[member.Name]; ok {
					continue
				}
			}
			keep = append(keep, member)
		}
		if len(keep) == 0 {
			// 全被精简掉的容器不再写出空壳。
			continue
		}

		file, err := os.Open(source)
		if err != nil {
			return nil, fmt.Errorf("打开包内容器 %s 失败: %w", container.Name, err)
		}
		destination := filepath.Join(outputDir, container.Name)
		if _, err := os.Lstat(destination); err == nil {
			file.Close()
			return nil, fmt.Errorf("输出目录里已存在同名容器: %s", container.Name)
		}
		partial := destination + ".part"
		out, err := os.Create(partial)
		if err != nil {
			file.Close()
			return nil, err
		}
		copyErr := npk.CopySubset(out, file, keep)
		closeSource := file.Close()
		closeOut := out.Close()
		if copyErr != nil || closeSource != nil || closeOut != nil {
			os.Remove(partial)
			if copyErr != nil {
				return nil, fmt.Errorf("写出容器 %s 失败: %w", container.Name, copyErr)
			}
			if closeSource != nil {
				return nil, fmt.Errorf("关闭容器 %s 失败: %w", container.Name, closeSource)
			}
			return nil, fmt.Errorf("关闭输出 %s 失败: %w", container.Name, closeOut)
		}
		if err := os.Rename(partial, destination); err != nil {
			os.Remove(partial)
			return nil, fmt.Errorf("发布容器 %s 失败: %w", container.Name, err)
		}
		result.Containers++
		result.Written += len(keep)
	}

	if err := writeNPKPruneReport(outputDir, report); err != nil {
		return nil, err
	}
	return result, nil
}

// writeNPKPruneReport records what was dropped and why. A silent prune is
// indistinguishable from a bug that lost artwork.
func writeNPKPruneReport(dir string, dropped []ImportNPKEntry) error {
	sort.Slice(dropped, func(i, j int) bool {
		if dropped[i].Container != dropped[j].Container {
			return dropped[i].Container < dropped[j].Container
		}
		return dropped[i].Name < dropped[j].Name
	})
	payload := struct {
		Version int              `json:"version"`
		Note    string           `json:"note"`
		Dropped []ImportNPKEntry `json:"dropped"`
	}{
		Version: 1,
		Note:    "这些图片在目标客户端里已有同名同字节的副本，已从本包容器中移除；目标目录未被修改。",
		Dropped: dropped,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "npk-prune.json"), append(data, '\n'), 0o644)
}

// planNPK builds the prune plan. It never writes anything.
func planNPK(packageRoot, targetDir string) ([]importNPKContainer, []ImportNPKEntry, []string, error) {
	warnings := []string{}
	packageDir := filepath.Join(packageRoot, "npk")
	if info, err := os.Stat(packageDir); err != nil || !info.IsDir() {
		return nil, nil, []string{"产物里没有 npk/ 目录，没有可精简的容器"}, nil
	}

	containers, err := collectNPKContainers(packageDir, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(containers) == 0 {
		return nil, nil, []string{"产物里没有可读的 NPK 容器"}, nil
	}

	// 先收集"我们关心的名字"，再扫目标目录。反过来做会把上百万个目标名字
	// 全留在内存里，而真正要判定的只有包里那几百个。
	wanted := make(map[string]struct{})
	for _, path := range containers {
		file, err := npk.Open(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过损坏的容器 %s: %v", filepath.Base(path), err))
			continue
		}
		for _, entry := range file.Entries {
			wanted[entry.Name] = struct{}{}
		}
	}

	targets, targetWarnings, err := scanNPKTargets(targetDir, wanted)
	if err != nil {
		return nil, nil, nil, err
	}
	warnings = append(warnings, targetWarnings...)

	entries := make([]ImportNPKEntry, 0)
	kept := make([]importNPKContainer, 0, len(containers))
	for _, path := range containers {
		info, err := os.Stat(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过容器 %s: %v", filepath.Base(path), err))
			continue
		}
		parsed, err := npk.Open(path)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		kept = append(kept, importNPKContainer{
			Name: name, Size: info.Size(), ModTime: info.ModTime(), Entries: parsed.Entries,
		})
		for _, member := range parsed.Entries {
			item := ImportNPKEntry{
				Key:       name + "\x00" + member.Name,
				Container: name,
				Name:      member.Name,
				Size:      member.Size,
			}
			target, ok := targets[member.Name]
			if !ok {
				item.State = "target-missing"
				entries = append(entries, item)
				continue
			}
			item.TargetNPK = filepath.Base(target.Path)
			same, err := npkMembersEqual(path, member, target)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("比对 %s 失败: %v", member.Name, err))
				item.State = "different"
				entries = append(entries, item)
				continue
			}
			if same {
				item.State = "identical"
				item.Selected = true
			} else {
				item.State = "different"
			}
			entries = append(entries, item)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Container != entries[j].Container {
			return entries[i].Container < entries[j].Container
		}
		return entries[i].Name < entries[j].Name
	})
	return kept, entries, warnings, nil
}

func collectNPKContainers(dir string, recursive bool) ([]string, error) {
	result := []string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !recursive && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".npk") {
			return nil
		}
		result = append(result, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

// scanNPKTargets maps the wanted image names to where the client keeps them.
// Only names in wanted are retained, so memory follows the package, not the
// client's entire image library.
func scanNPKTargets(dir string, wanted map[string]struct{}) (map[string]importNPKTarget, []string, error) {
	warnings := []string{}
	targets := make(map[string]importNPKTarget, len(wanted))
	containers, err := collectNPKContainers(dir, true)
	if err != nil {
		return nil, nil, fmt.Errorf("扫描目标 NPK 目录失败: %w", err)
	}
	if len(containers) > importNPKMaxContainers {
		return nil, nil, fmt.Errorf("目标目录里有 %d 个 NPK，超过上限 %d；请确认选的是 ImagePacks2",
			len(containers), importNPKMaxContainers)
	}
	duplicates := 0
	for _, path := range containers {
		parsed, err := npk.Open(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过无法解析的目标容器 %s: %v", filepath.Base(path), err))
			continue
		}
		for _, entry := range parsed.Entries {
			if _, ok := wanted[entry.Name]; !ok {
				continue
			}
			if _, exists := targets[entry.Name]; exists {
				// 同一路径出现在多个容器里时，谁生效取决于客户端加载顺序；
				// 这里只认第一个，并把重复计数报出来。
				duplicates++
				continue
			}
			targets[entry.Name] = importNPKTarget{Path: path, Offset: entry.Offset, Size: entry.Size}
		}
	}
	if duplicates > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"目标目录里有 %d 个同名图片分布在多个容器中，比对只取第一个", duplicates))
	}
	return targets, warnings, nil
}

func npkMembersEqual(packagePath string, member npk.Entry, target importNPKTarget) (bool, error) {
	if member.Size != target.Size {
		return false, nil
	}
	if member.Size == 0 {
		return true, nil
	}
	packageFile, err := os.Open(packagePath)
	if err != nil {
		return false, err
	}
	defer packageFile.Close()
	targetFile, err := os.Open(target.Path)
	if err != nil {
		return false, err
	}
	defer targetFile.Close()

	left := make([]byte, importNPKCompareBuf)
	right := make([]byte, importNPKCompareBuf)
	for offset := int64(0); offset < member.Size; offset += importNPKCompareBuf {
		size := int64(importNPKCompareBuf)
		if remaining := member.Size - offset; remaining < size {
			size = remaining
		}
		if _, err := packageFile.ReadAt(left[:size], member.Offset+offset); err != nil && err != io.EOF {
			return false, err
		}
		if _, err := targetFile.ReadAt(right[:size], target.Offset+offset); err != nil && err != io.EOF {
			return false, err
		}
		if string(left[:size]) != string(right[:size]) {
			return false, nil
		}
	}
	return true, nil
}
