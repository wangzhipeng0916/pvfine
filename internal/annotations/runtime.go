package annotations

import (
	"fmt"
	"os"
	"path/filepath"

	appconfig "pvfine/config"
	"pvfine/internal/appdirs"
)

const sourceRelativePath = "config/annotations.json"

// LoadRulesFile reads and validates a hand-maintained rule file together with
// its sibling lists.json, without merging the generated rule sets. The
// annotation editor uses it so a save never writes the bulk path or section
// rules back into annotations.json.
func LoadRulesFile(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("读取标注规则失败: %w", err)
	}
	document, err := parseDocument(data)
	if err != nil {
		return Document{}, err
	}
	listsPath := filepath.Join(filepath.Dir(path), "lists.json")
	if listsData, listsErr := os.ReadFile(listsPath); listsErr == nil {
		lists, err := ParseLists(listsData)
		if err != nil {
			return Document{}, err
		}
		document.Relations = lists.Relations
	} else if !os.IsNotExist(listsErr) {
		return Document{}, fmt.Errorf("读取列表配置失败: %w", listsErr)
	} else if document.Relations == nil {
		lists, err := ParseLists(appconfig.ListsJSON)
		if err != nil {
			return Document{}, err
		}
		document.Relations = lists.Relations
	}
	if err := Validate(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

// LoadFile validates and compiles a rule document from disk. A paths.json or
// sections.json next to the rule file overrides the built-in generated rules, so
// a runtime mirror can ship its own notes.
func LoadFile(path string) (*Engine, error) {
	document, err := LoadRulesFile(path)
	if err != nil {
		return nil, err
	}
	for _, generated := range generatedRuleFiles() {
		data := generated.data
		sibling := filepath.Join(filepath.Dir(path), generated.name)
		if override, readErr := os.ReadFile(sibling); readErr == nil {
			data = override
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("读取 %s 失败: %w", generated.label, readErr)
		}
		if err := mergeGeneratedRules(&document, data, generated.label); err != nil {
			return nil, err
		}
	}
	if err := Validate(document); err != nil {
		return nil, err
	}
	return Compile(document)
}

// RuntimePath is the writable rule mirror used by packaged applications.
func RuntimePath() (string, error) {
	return appdirs.DataFile("annotations.json")
}

func RuntimeListsPath() (string, error) {
	return appdirs.DataFile("lists.json")
}

// FindSourcePath locates the repository rule file for local development.
func FindSourcePath() (string, bool) {
	starts := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}
	seen := make(map[string]struct{})
	for _, start := range starts {
		for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
			if _, ok := seen[dir]; !ok {
				seen[dir] = struct{}{}
				candidate := filepath.Join(dir, filepath.FromSlash(sourceRelativePath))
				if fileExists(filepath.Join(dir, "go.mod")) && fileExists(candidate) {
					return candidate, true
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	return "", false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
