package importing

import (
	"fmt"

	"pvfine/internal/extraction"
)

const (
	// kindExtraction 标记来源是一份带 manifest.json 的提取产物。
	kindExtraction = "extraction"
	// originManifest 说明候选的路径与类型都来自 manifest，而不是按目录结构猜测。
	originManifest = "manifest"
)

// FromExtraction 读取 <root>/manifest.json 并把每个条目解析成 Candidate。
//
// 这里只解析路径，不打开载荷：文件缺失是计划阶段的问题，应该让那一条判 invalid，
// 而不是让整包打不开。路径一律经 Manifest.DestPath 解析，manifest.Dest 是用户可编辑的，
// 直接拼接会把越界路径带进来。
func FromExtraction(root string) (*Source, error) {
	manifest, err := extraction.ReadManifest(root)
	if err != nil {
		return nil, err
	}
	if len(manifest.Files) == 0 {
		return nil, fmt.Errorf("manifest 没有任何条目")
	}

	source := &Source{
		Kind:       kindExtraction,
		Root:       root,
		Manifest:   manifest,
		Candidates: make([]Candidate, 0, len(manifest.Files)),
	}
	for _, file := range manifest.Files {
		sourcePath, err := manifest.DestPath(root, file)
		if err != nil {
			return nil, fmt.Errorf("解析 %q 的磁盘路径失败: %w", file.Path, err)
		}
		source.Candidates = append(source.Candidates, Candidate{
			ArchivePath:   file.Path,
			SourcePath:    sourcePath,
			Form:          file.Form,
			DataType:      file.DataType,
			Size:          int64(file.Size),
			Origin:        originManifest,
			Category:      file.Category,
			AutoCompleted: file.AutoCompleted,
		})
	}
	return source, nil
}
