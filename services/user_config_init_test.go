package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"pvfine/internal/appdirs"
	"pvfine/internal/extraction"
)

func TestEnsureConfigFileCreatesOnlyWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	created, err := ensureConfigFile(path, []byte("{}\n"))
	if err != nil || !created {
		t.Fatalf("首次应创建: created=%t err=%v", created, err)
	}
	if err := os.WriteFile(path, []byte(`{"user":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = ensureConfigFile(path, []byte("{}\n"))
	if err != nil || created {
		t.Fatalf("已存在时不应改写: created=%t err=%v", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"user":true}` {
		t.Errorf("用户内容被覆盖: %q", data)
	}
}

func TestBookmarkStartupInitializesCustomDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	service := newBookmarkService(path)
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	document, err := service.LoadBookmarks()
	if err != nil {
		t.Fatalf("初始化后的书签文件应能加载: %v", err)
	}
	if len(document.Books) != 1 || !document.Books[0].Builtin {
		t.Errorf("空用户文件应只显示内置书签簿, got %+v", document.Books)
	}
}

func TestFileSetStartupInitializesDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-sets.json")
	service := newFileSetService(path)
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	document, err := service.LoadFileSets()
	if err != nil {
		t.Fatalf("初始化后的文件集应能加载: %v", err)
	}
	if document.Version != fileSetDocumentVersion || len(document.FileSets) != 0 {
		t.Errorf("空文件集文档不符: %+v", document)
	}
}

func TestSettingsStartupInitializesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	service := newSettingsService(path)
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.GetSettings()
	if err != nil {
		t.Fatalf("初始化后的设置应能加载: %v", err)
	}
	if !reflect.DeepEqual(settings, DefaultAppSettings()) {
		t.Errorf("初始化文件应等于默认设置, got %+v", settings)
	}
}

func TestRuleStartupInitializesOverlayDocuments(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(appdirs.DataDirEnv, dataDir)
	ensureRuleOverlayFiles()
	for _, name := range []string{"annotations.user.json", "rendering.user.json"} {
		data, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatalf("%s 未初始化: %v", name, err)
		}
		var document struct {
			Version int               `json:"version"`
			Rules   []json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("%s 不是合法 JSON: %v", name, err)
		}
		if document.Version != 1 || len(document.Rules) != 0 {
			t.Errorf("%s 应为空的 version=1 覆盖文档, got %+v", name, document)
		}
	}
}

func TestExtractionStartupInitializesCatalogDocument(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(appdirs.DataDirEnv, dataDir)
	previous := extractionCatalog
	extractionCatalog = &catalogSource{}
	t.Cleanup(func() { extractionCatalog = previous })

	service := &ExtractionService{}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, extractionCatalogFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("边目录文件未初始化: %v", err)
	}
	var document extraction.Document
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("初始化的边目录不是合法 JSON: %v", err)
	}
	if document.Version != 1 || len(document.Edges) != 0 || len(document.Categories) != 0 {
		t.Errorf("初始化为空的部分文档, got %+v", document)
	}
	status, err := service.CatalogStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Source != "external" || status.ExternalEdgeCount != 0 || status.ExternalCategoryCount != 0 {
		t.Errorf("空外部文件应显示为已就绪但未声明覆盖, got %+v", status)
	}
	if status.EdgeCount != status.BuiltinEdgeCount {
		t.Errorf("空外部文件的有效边数应等于内置边数: %d vs %d", status.EdgeCount, status.BuiltinEdgeCount)
	}

	// 启动初始化只在缺失时写；已有内容（包括用户的空修改）必须原样保留。
	custom := []byte(`{"version":1,"description":"mine","edges":[]}` + "\n")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(custom) {
		t.Errorf("已存在的文件被启动初始化覆盖: %q", after)
	}
}
