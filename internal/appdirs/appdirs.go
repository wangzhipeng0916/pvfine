// Package appdirs 决定 pvfine 的两类落点：能丢的缓存进系统临时目录，不能丢的数据
// 跟着 exe 走。两者都能用环境变量覆盖，测试隔离与绿色部署都靠这两个口子。
package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DataDirEnv 覆盖数据目录，供测试与便携部署使用。
	DataDirEnv = "PVFINE_DATA_DIR"
	// CacheDirEnv 覆盖缓存目录，供测试与绿色部署使用。
	CacheDirEnv = "PVFINE_CACHE_DIR"

	dataDirName = "pvfine-data"
	appName     = "pvfine"
)

// DataDir 返回持久数据目录：优先环境变量，其次 exe 同级的 pvfine-data/。
// exe 目录不可写（装在 Program Files 之类的只读位置）时回退到旧的用户配置目录，
// 设置写不出去不该成为应用起不来的理由。
func DataDir() (string, error) {
	if override := os.Getenv(DataDirEnv); override != "" {
		return ensure(override)
	}
	if exe, err := os.Executable(); err == nil {
		if dir, err := ensure(filepath.Join(filepath.Dir(exe), dataDirName)); err == nil {
			return dir, nil
		}
	}
	legacy, err := LegacyDataDir()
	if err != nil {
		return "", err
	}
	return ensure(legacy)
}

// DataFile 返回数据目录下的一个文件路径。
func DataFile(name string) (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// CacheDir 返回可丢弃缓存目录（系统临时目录下的 pvfine）。本机临时目录是内存盘，
// 重启即清空，所以这里的任何东西都不能承载用户数据。
func CacheDir() (string, error) {
	if override := os.Getenv(CacheDirEnv); override != "" {
		return ensure(override)
	}
	return ensure(filepath.Join(os.TempDir(), appName))
}

// CacheDirIn 返回缓存根下的一个子目录，并确保它存在。
func CacheDirIn(elem ...string) (string, error) {
	dir, err := joinCache(elem...)
	if err != nil {
		return "", err
	}
	return ensure(dir)
}

// CacheFile 返回缓存根下的一个文件路径，并确保其所在目录存在。调用方拿到路径
// 就能直接写，不必各自再补一次 MkdirAll。
func CacheFile(elem ...string) (string, error) {
	path, err := joinCache(elem...)
	if err != nil {
		return "", err
	}
	if _, err := ensure(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, nil
}

func joinCache(elem ...string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, elem...)...), nil
}

// LegacyDataDir 是 v0.16 及之前放配置的位置，只用于回退与迁移来源。
func LegacyDataDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("获取用户配置目录失败: %w", err)
	}
	return filepath.Join(configDir, appName), nil
}

// MigrateLegacyData 把旧配置目录里的数据搬进数据目录，返回搬动的条目名。
// 只在目标不存在时移动，任何一步出错都保留原件；可重复调用。
func MigrateLegacyData() []string {
	target, err := DataDir()
	if err != nil {
		return nil
	}
	source, err := LegacyDataDir()
	if err != nil || source == target {
		return nil
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil
	}
	var moved []string
	for _, entry := range entries {
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(target, entry.Name())
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := os.Rename(from, to); err != nil {
			continue
		}
		moved = append(moved, entry.Name())
	}
	return moved
}

func ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	return dir, nil
}
