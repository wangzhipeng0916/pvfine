package extraction_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// TestTextExportIsImportableAndFaithful pins the default write form: the
// product is the decompiled script at its original path — no .txt suffix, valid
// UTF-8, and semantically identical to the archive entry. That is what makes an
// extract → edit → 源码导入（文本模式）round trip possible at all.
func TestTextExportIsImportableAndFaithful(t *testing.T) {
	archive := openRealArchive(t)
	scanner := newRealScanner(t, archive)
	seed, ok := archive.Find(realDungeonSeed)
	if !ok {
		t.Skip("种子不存在")
	}
	closure, err := scanner.Closure([]int32{seed}, extraction.Options{})
	if err != nil {
		t.Fatalf("闭包失败: %v", err)
	}
	plan, err := extraction.BuildPlan(archive, scanner.Catalog(), closure, extraction.Selection{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Selection.Form != extraction.FormText {
		t.Fatalf("默认形态应为文本, got %q", plan.Selection.Form)
	}
	root := t.TempDir()
	result, err := extraction.Export(archive, closure, extraction.ExportOptions{Dir: root, Plan: plan})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.TextFiles == 0 {
		t.Fatal("没有条目以文本形态写出")
	}
	if result.RawFiles != 0 {
		t.Logf("注意：%d 个条目因无文本形态而写成了原字节", result.RawFiles)
	}

	scriptFile := ""
	for _, index := range plan.Exported() {
		relative, err := plan.DestPath(index)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, extraction.DefaultSubdir, filepath.FromSlash(relative))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", relative, err)
		}
		if extraction.EffectiveForm(archive, index, plan.Selection.Form) == extraction.FormRaw {
			continue
		}
		if !utf8.Valid(data) {
			t.Errorf("%s 不是合法 UTF-8，导入的文本模式会拒收", relative)
			continue
		}
		// 产物必须是文本而不是原字节：脚本原文里一定有可读的 section 标签。
		if !strings.Contains(string(data), "[") && !strings.Contains(string(data), "\n") {
			t.Errorf("%s 看不出文本形态", relative)
		}
		want, err := archive.ScriptViewOf(index)
		if err != nil {
			t.Fatalf("ScriptViewOf(%s): %v", relative, err)
		}
		got := pvf.ParseScriptView(string(data))
		if diff := firstViewDifference(want, got); diff != "" {
			t.Errorf("%s 文本与归档语义不一致: %s", relative, diff)
		}
		if scriptFile == "" && strings.HasSuffix(relative, ".mob") {
			scriptFile = string(data)
		}
	}
	// 原字节形态下逐字节必须一致（回填/诊断用）。
	rawRoot := t.TempDir()
	rawPlan, err := extraction.BuildPlan(archive, scanner.Catalog(), closure,
		extraction.Selection{Form: extraction.FormRaw})
	if err != nil {
		t.Fatalf("BuildPlan(raw): %v", err)
	}
	rawResult, err := extraction.Export(archive, closure, extraction.ExportOptions{Dir: rawRoot, Plan: rawPlan})
	if err != nil {
		t.Fatalf("Export(raw): %v", err)
	}
	if rawResult.RawFiles != rawResult.Files {
		t.Errorf("原字节形态下所有条目都应是 raw: %d/%d", rawResult.RawFiles, rawResult.Files)
	}
	for _, index := range rawPlan.Exported() {
		relative, err := rawPlan.DestPath(index)
		if err != nil {
			t.Fatal(err)
		}
		onDisk, err := os.ReadFile(filepath.Join(rawRoot, extraction.DefaultSubdir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("读取 %s: %v", relative, err)
		}
		raw, err := archive.RawBytes(index)
		if err != nil {
			t.Fatal(err)
		}
		if string(onDisk) != string(raw) {
			t.Errorf("%s 原字节形态与原包不一致", relative)
		}
	}
	if scriptFile != "" && !strings.Contains(scriptFile, "\n") {
		t.Error("采样到的脚本产物没有换行，形态可疑")
	}
}

func firstViewDifference(want, got pvf.ScriptView) string {
	if len(want.Elements) != len(got.Elements) {
		return "元素数 " + itoaInt(len(want.Elements)) + " vs " + itoaInt(len(got.Elements))
	}
	for i := range want.Elements {
		a, b := want.Elements[i], got.Elements[i]
		if a.Kind != b.Kind || a.TokenType != b.TokenType || a.Value != b.Value ||
			a.Section != b.Section || a.SectionID != b.SectionID || a.Index != b.Index {
			return "第 " + itoaInt(i) + " 个元素不同"
		}
	}
	return ""
}

func itoaInt(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}
