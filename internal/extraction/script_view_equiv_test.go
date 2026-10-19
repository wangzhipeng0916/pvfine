package extraction_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

// TestTokenStreamViewMatchesTextPath is the gate for the P2 read path: the
// scanner now consumes the archive's own token stream instead of decompiling to
// text and re-lexing it. Both routes must project exactly the same elements —
// kind, token type, value, section, section id, section path and index — or the
// whole graph changes shape for reasons nobody asked for.
//
// Editor offsets (Start/End) are deliberately excluded: they describe positions
// in the decompiled text, which the token route never materialises.
func TestTokenStreamViewMatchesTextPath(t *testing.T) {
	archive := openRealArchive(t)
	// 默认抽样 1600 个条目；PVF_EQUIV_SAMPLE 可调大（全库深抽查用）。
	const defaultSampleCap = 4000
	sampleCap := defaultSampleCap
	if raw := strings.TrimSpace(os.Getenv("PVF_EQUIV_SAMPLE")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			sampleCap = n
		}
	}
	var (
		checked, skipped, normalized int
		failures                     []string
	)
	count := archive.FileCount()
	stride := count/int32(sampleCap) + 1
	for i := int32(0); i < count && checked < sampleCap; i += stride {
		if archive.File(i).DataType != pvf.TypeScript {
			continue
		}
		path := archive.FullPath(i)
		if strings.HasSuffix(strings.ToLower(path), ".lst") {
			// 列表文件在扫描器里直接跳过，但也要确认两条路径一致。
			skipped++
		}
		text, err := archive.Text(i)
		if err != nil {
			continue
		}
		want := pvf.ParseScriptView(text)
		got, err := archive.ScriptViewOf(i)
		if err != nil {
			t.Fatalf("ScriptViewOf(%s) 失败: %v", path, err)
		}
		checked++
		if diff, canonicalized := firstElementDiff(want, got); diff != "" {
			if len(failures) < 6 {
				failures = append(failures, path+": "+diff)
			}
		} else if canonicalized {
			normalized++
		}
	}
	if checked == 0 {
		t.Skip("没有可比较的 TypeScript 条目")
	}
	for _, line := range failures {
		t.Errorf("token 路径与文本路径不一致: %s", line)
	}
	t.Logf("抽样 %d 个 TypeScript 条目（其中 .lst %d），不一致 %d 个；%d 个仅差无效 UTF-8 字节的规范化",
		checked, skipped, len(failures), normalized)
}

// firstElementDiff returns a human-readable difference, or "" when the two
// projections agree. The second result reports whether they only agreed after
// canonicalizing invalid UTF-8.
//
// The one sanctioned divergence: the text route runs the decompiled string
// through []rune, which rewrites every invalid byte as U+FFFD, while the token
// route hands back the archive's own bytes. Keeping the raw bytes is the more
// faithful behaviour — those strings live in [name] fields, and a byte-exact
// value can still match archive entries while a replacement-character one
// cannot. Canonicalizing both sides here keeps the gate focused on structure:
// any difference that survives is a real one.
func firstElementDiff(want, got pvf.ScriptView) (string, bool) {
	canonicalized := false
	if len(want.Elements) != len(got.Elements) {
		return "元素数 " + strconv.Itoa(len(want.Elements)) + " vs " + strconv.Itoa(len(got.Elements)), false
	}
	for i := range want.Elements {
		a, b := want.Elements[i], got.Elements[i]
		aValue, bValue := a.Value, b.Value
		if aValue != bValue {
			if string([]rune(aValue)) == string([]rune(bValue)) {
				canonicalized = true
				aValue, bValue = string([]rune(aValue)), string([]rune(bValue))
			}
		}
		if a.Kind != b.Kind || a.TokenType != b.TokenType || aValue != bValue ||
			a.Section != b.Section || a.SectionID != b.SectionID || a.Index != b.Index ||
			strings.Join(a.SectionPath, "/") != strings.Join(b.SectionPath, "/") {
			return "第 " + strconv.Itoa(i) + " 个元素: 文本{" + a.Kind + " tt=" + strconv.Itoa(int(a.TokenType)) +
				" val=" + clipForTest(a.Value) + " sec=" + a.Section + " id=" + strconv.Itoa(a.SectionID) +
				" idx=" + strconv.Itoa(a.Index) + "} vs token{" + b.Kind + " tt=" + strconv.Itoa(int(b.TokenType)) +
				" val=" + clipForTest(b.Value) + " sec=" + b.Section + " id=" + strconv.Itoa(b.SectionID) +
				" idx=" + strconv.Itoa(b.Index) + "}", false
		}
	}
	return "", canonicalized
}

func clipForTest(s string) string {
	quoted := strconv.Quote(s)
	if len(quoted) > 60 {
		return quoted[:60] + `…`
	}
	return quoted
}
