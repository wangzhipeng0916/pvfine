package extraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoManifest reports that a directory is not an extraction product.
var ErrNoManifest = errors.New("extraction: 目录下没有 manifest.json")

// ReadManifest parses <root>/manifest.json and validates the fields an
// importer depends on. It never reads the payload files: whether a Dest exists
// is a plan-time question, and a missing file invalidates one entry rather than
// the whole package.
//
// The manifest is the only contract between extraction and import. Both sides
// live in this package so a new field cannot drift into two structs.
func ReadManifest(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if os.IsNotExist(err) {
		return nil, ErrNoManifest
	}
	if err != nil {
		return nil, fmt.Errorf("读取 manifest.json 失败: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析 manifest.json 失败: %w", err)
	}
	// A newer schema may carry fields whose absence changes what the files
	// mean. Refusing is the only honest answer: guessing would import a
	// package we do not understand.
	if manifest.SchemaVersion != SemanticVersion {
		return nil, fmt.Errorf("不支持的 manifest 版本 %d（本版本支持 %d）",
			manifest.SchemaVersion, SemanticVersion)
	}
	// 载荷目录由导出时决定，默认 "pvf"。不记录它的话，CLI 换一个名字导出就再也
	// 找不到任何文件；旧 manifest 没有这个字段，按默认值补。
	manifest.Subdir = strings.Trim(strings.ReplaceAll(strings.TrimSpace(manifest.Subdir), "\\", "/"), "/")
	if manifest.Subdir == "" {
		manifest.Subdir = DefaultSubdir
	}
	if err := validateArchivePath(manifest.Subdir); err != nil {
		return nil, fmt.Errorf("manifest.subdir 非法: %w", err)
	}
	for index := range manifest.Files {
		file := &manifest.Files[index]
		if err := validateArchivePath(file.Path); err != nil {
			return nil, fmt.Errorf("第 %d 个条目的 path 非法: %w", index, err)
		}
		if file.Dest == "" {
			// 旧版或手写的 manifest 可能省略 Dest。Dest 与 path 同基准（都相对
			// manifest.subdir），所以缺省就是 path。
			file.Dest = file.Path
		}
		if err := validateArchivePath(file.Dest); err != nil {
			return nil, fmt.Errorf("第 %d 个条目的 dest 非法: %w", index, err)
		}
	}
	return &manifest, nil
}

// DestPath returns the absolute on-disk path of one exported payload and
// refuses anything that would escape root. Callers must use this instead of
// joining Dest themselves: Dest comes from a file the user can edit.
//
// Dest is relative to manifest.Subdir (`pvf` by default), not to root — the
// exporter writes through resolveUnder(root, subdir, dest).
func (m *Manifest) DestPath(root string, file ExportedFile) (string, error) {
	if err := validateArchivePath(file.Dest); err != nil {
		return "", fmt.Errorf("dest 非法: %w", err)
	}
	subdir := strings.Trim(strings.ReplaceAll(strings.TrimSpace(m.Subdir), "\\", "/"), "/")
	if subdir == "" {
		subdir = DefaultSubdir
	}
	if err := validateArchivePath(subdir); err != nil {
		return "", fmt.Errorf("subdir 非法: %w", err)
	}
	rootAbs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(subdir)))
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(file.Dest))
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("dest 越出导出目录: %s", file.Dest)
	}
	return target, nil
}

// validateArchivePath accepts a slash-separated relative path with no empty,
// "." or ".." segment. Archive paths may contain spaces and non-ASCII text;
// only segments that change meaning are rejected.
func validateArchivePath(p string) error {
	if p == "" {
		return errors.New("路径为空")
	}
	if p != strings.TrimSpace(p) {
		return errors.New("路径含首尾空白")
	}
	if strings.ContainsRune(p, '\\') {
		return errors.New("路径含反斜杠")
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("路径是绝对路径")
	}
	if strings.ContainsRune(p, ':') {
		return errors.New("路径含盘符")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return errors.New("路径含控制字符")
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("路径片段非法: %q", segment)
		}
	}
	return nil
}
