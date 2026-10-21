package services

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"pvfine/internal/npk"
)

// writeTestNPK lays out a container by hand. It must not go through
// npk.CopySubset, or the prune plan would be validated against its own output.
func writeTestNPK(t *testing.T, path string, members map[string][]byte) {
	t.Helper()
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)

	offset := int64(20 + 264*len(names))
	offsets := make([]int64, len(names))
	for index, name := range names {
		offset = (offset + 3) &^ 3
		offsets[index] = offset
		offset += int64(len(members[name]))
	}

	var buffer bytes.Buffer
	header := make([]byte, 20)
	copy(header, "NeoplePack_Bill")
	binary.LittleEndian.PutUint32(header[16:], uint32(len(names)))
	buffer.Write(header)
	for index, name := range names {
		raw, err := npk.EncodeName(name)
		if err != nil {
			t.Fatalf("EncodeName(%s): %v", name, err)
		}
		entry := make([]byte, 264)
		binary.LittleEndian.PutUint32(entry[0:], uint32(offsets[index]))
		binary.LittleEndian.PutUint32(entry[4:], uint32(len(members[name])))
		copy(entry[8:], raw[:])
		buffer.Write(entry)
	}
	for index, name := range names {
		for buffer.Len() < int(offsets[index]) {
			buffer.WriteByte(0)
		}
		buffer.Write(members[name])
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("写入测试容器 %s: %v", path, err)
	}
}

// TestPlanNPKClassifiesAgainstTarget pins the three states that matter:
// byte-identical is prunable, same-name-different-bytes is a conflict that must
// stay, and a name the target lacks is kept.
func TestPlanNPKClassifiesAgainstTarget(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "product")
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(packageRoot, "npk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	same := []byte("same-bytes")
	writeTestNPK(t, filepath.Join(packageRoot, "npk", "a.npk"), map[string][]byte{
		"sprite/x/same.img": same,
		"sprite/x/diff.img": []byte("package-version"),
		"sprite/x/only.img": []byte("only-in-package"),
	})
	writeTestNPK(t, filepath.Join(targetDir, "b.npk"), map[string][]byte{
		"sprite/x/same.img": same,
		"sprite/x/diff.img": []byte("target-version"),
	})

	containers, entries, warnings, err := planNPK(packageRoot, targetDir)
	if err != nil {
		t.Fatalf("planNPK: %v", err)
	}
	if len(containers) != 1 || containers[0].Name != "a.npk" {
		t.Fatalf("容器列表不对: %+v", containers)
	}
	if len(warnings) != 0 {
		t.Fatalf("不应有警告: %v", warnings)
	}

	byName := make(map[string]ImportNPKEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if got := byName["sprite/x/same.img"]; got.State != "identical" || !got.Selected {
		t.Fatalf("同字节应为可精简: %+v", got)
	}
	if got := byName["sprite/x/diff.img"]; got.State != "different" || got.Selected {
		t.Fatalf("同名不同字节必须保留: %+v", got)
	}
	if got := byName["sprite/x/only.img"]; got.State != "target-missing" || got.Selected {
		t.Fatalf("目标没有的必须保留: %+v", got)
	}
}

// TestPlanNPKWithoutPackageContainers: an import product that carries no npk/
// must not look like "everything is a duplicate".
func TestPlanNPKWithoutPackageContainers(t *testing.T) {
	root := t.TempDir()
	containers, entries, warnings, err := planNPK(root, root)
	if err != nil {
		t.Fatalf("planNPK: %v", err)
	}
	if len(containers) != 0 || len(entries) != 0 {
		t.Fatalf("没有容器时不应产出条目: %+v %+v", containers, entries)
	}
	if len(warnings) == 0 {
		t.Fatal("没有 npk/ 目录时必须给出说明")
	}
}
