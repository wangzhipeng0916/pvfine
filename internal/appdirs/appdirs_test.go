package appdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOverridesWin(t *testing.T) {
	data := t.TempDir()
	cache := t.TempDir()
	t.Setenv(DataDirEnv, data)
	t.Setenv(CacheDirEnv, cache)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	if got != data {
		t.Fatalf("DataDir() = %q, want %q", got, data)
	}
	gotFile, err := DataFile("settings.json")
	if err != nil {
		t.Fatalf("DataFile() error: %v", err)
	}
	if gotFile != filepath.Join(data, "settings.json") {
		t.Fatalf("DataFile() = %q", gotFile)
	}

	gotCache, err := CacheFile("advanced-search", "index.db")
	if err != nil {
		t.Fatalf("CacheFile() error: %v", err)
	}
	if gotCache != filepath.Join(cache, "advanced-search", "index.db") {
		t.Fatalf("CacheFile() = %q", gotCache)
	}
}

func TestDefaults(t *testing.T) {
	t.Setenv(DataDirEnv, "")
	t.Setenv(CacheDirEnv, "")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	if want := filepath.Join(filepath.Dir(exe), "pvfine-data"); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}

	cache, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error: %v", err)
	}
	if want := filepath.Join(os.TempDir(), "pvfine"); cache != want {
		t.Fatalf("CacheDir() = %q, want %q", cache, want)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("CacheDir() 没有创建目录 %q: %v", cache, err)
	}
}

func TestCacheHelpersCreateParents(t *testing.T) {
	cache := t.TempDir()
	t.Setenv(CacheDirEnv, cache)

	dir, err := CacheDirIn("archive-index")
	if err != nil {
		t.Fatalf("CacheDirIn() error: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("CacheDirIn() 没有创建 %q: %v", dir, err)
	}

	file, err := CacheFile("search-index", "abc.json.gz")
	if err != nil {
		t.Fatalf("CacheFile() error: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(file)); err != nil || !info.IsDir() {
		t.Fatalf("CacheFile() 没有创建父目录: %v", err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("CacheFile() 返回的路径不可写: %v", err)
	}
}

func TestMigrateLegacyData(t *testing.T) {
	data := t.TempDir()
	legacy := t.TempDir()
	// Windows 上 UserConfigDir 取 APPDATA，Linux 上取 XDG_CONFIG_HOME，两个都指过去。
	t.Setenv(DataDirEnv, data)
	t.Setenv("APPDATA", legacy)
	t.Setenv("XDG_CONFIG_HOME", legacy)

	legacyDir := filepath.Join(legacy, "pvfine")
	if err := os.MkdirAll(filepath.Join(legacyDir, "scripts"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(data, "bookmarks.json"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	moved, err := MigrateLegacyData()
	if err != nil {
		t.Fatalf("MigrateLegacyData() error: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("MigrateLegacyData() = %v, want settings.json 与 scripts", moved)
	}
	if _, err := os.Stat(filepath.Join(data, "settings.json")); err != nil {
		t.Fatalf("settings.json 未迁移: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "scripts")); err != nil {
		t.Fatalf("scripts/ 未迁移: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(data, "bookmarks.json"))); got != "keep" {
		t.Fatalf("已存在的目标被覆盖了: %q", got)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("旧文件应当移走而不是复制: %v", err)
	}
	if again, err := MigrateLegacyData(); err != nil || len(again) != 0 {
		t.Fatalf("二次调用应当无事可做, got %v / %v", again, err)
	}
}

func TestMovePathFallsBackToCopy(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "scripts", "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "scripts", "nested", "a.js"), []byte("code"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	to := filepath.Join(target, "scripts")
	// 跨盘卷时 os.Rename 会失败，这里直接验证兜底的整棵复制与删源。
	if err := copyPath(filepath.Join(source, "scripts"), to); err != nil {
		t.Fatalf("copyPath() error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(to, "nested", "a.js"))
	if err != nil {
		t.Fatalf("复制后读不到文件: %v", err)
	}
	if string(content) != "code" {
		t.Fatalf("复制内容 = %q, want %q", content, "code")
	}
	if err := os.RemoveAll(filepath.Join(source, "scripts")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "scripts")); !os.IsNotExist(err) {
		t.Fatalf("源目录应当被删掉: %v", err)
	}
}

func TestMigrateWithoutLegacyData(t *testing.T) {
	data := t.TempDir()
	legacy := t.TempDir()
	t.Setenv(DataDirEnv, data)
	t.Setenv("APPDATA", legacy)
	t.Setenv("XDG_CONFIG_HOME", legacy)

	moved, err := MigrateLegacyData()
	if err != nil || len(moved) != 0 {
		t.Fatalf("旧目录不存在时应当无事可做, got %v / %v", moved, err)
	}
	// 迁移不该顺手把旧目录建出来。
	legacyDir := filepath.Join(legacy, "pvfine")
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("MigrateLegacyData() 不该创建旧配置目录: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return content
}
