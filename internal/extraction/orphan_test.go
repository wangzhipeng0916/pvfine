package extraction

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// orphanFixture adds three kinds of unreferenced content so attribution can be
// told apart: one reachable only from an undeclared candidate, one only from a
// runtime-substituted template, and one nothing names at all.
func orphanFixture(t *testing.T) (*fixture, *Scanner, *ReverseIndex) {
	t.Helper()
	f := newFixtureFrom(t, []entry{
		// 未声明的 [mystery anim] 段提到它：声明该类就不再是孤儿。
		{"passiveobject/obj/hidden.ani", "[FRAME MAX]\n\t1\n"},
		// 只被一个 %s 模板提到：静态永远判不了，但不是没人要。
		{"passiveobject/obj/templated.ani", "[FRAME MAX]\n\t1\n"},
		// 谁都不提。
		{"passiveobject/obj/dead.ani", "[FRAME MAX]\n\t1\n"},
		{"passiveobject/obj/holder.obj", "[use animation]\n\t`door.ani`\n[weird]\n" +
			"\t`templat%s.ani`\n"},
	}, testDocument())
	scanner := f.scanner(t)
	indexes := make([]int32, 0, f.archive.FileCount())
	for index := int32(0); index < f.archive.FileCount(); index++ {
		indexes = append(indexes, index)
	}
	reverse, err := BuildReverseIndex(context.Background(), scanner, indexes, runtime.NumCPU())
	if err != nil {
		t.Fatalf("BuildReverseIndex: %v", err)
	}
	return f, scanner, reverse
}

func TestAttributeOrphansSplitsByCause(t *testing.T) {
	f, scanner, reverse := orphanFixture(t)
	attribution, err := AttributeOrphans(context.Background(), scanner, reverse, runtime.NumCPU())
	if err != nil {
		t.Fatalf("AttributeOrphans: %v", err)
	}
	index := func(path string) int32 {
		t.Helper()
		at, ok := f.archive.Find(path)
		if !ok {
			t.Fatalf("fixture 缺少 %s", path)
		}
		return at
	}
	if !attribution.undeclaredEdges[index("passiveobject/obj/hidden.ani")] {
		t.Errorf("hidden.ani 应由未声明候选解释, buckets=%v", attribution.Buckets)
	}
	if !attribution.templateNamed[index("passiveobject/obj/templated.ani")] {
		t.Errorf("templated.ani 应由 `templat%%s.ani` 模板解释, templates=%d degenerate=%d",
			attribution.Templates, attribution.Degenerate)
	}
	dead := index("passiveobject/obj/dead.ani")
	if !attribution.Unexplained(dead) {
		t.Errorf("dead.ani 才是真无人引用")
	}
	total := 0
	for bucket, count := range attribution.Buckets {
		if !strings.HasSuffix(string(bucket), "引用") && bucket != OrphanUndeclaredEdge &&
			bucket != OrphanTemplateNamed && bucket != OrphanFailedRef {
			t.Errorf("出现了未知归因档 %q", bucket)
		}
		total += count
	}
	if total != attribution.Total {
		t.Errorf("归因合计 %d 与孤儿总数 %d 不符", total, attribution.Total)
	}
	rows := attribution.Rows()
	if len(rows) == 0 {
		t.Fatal("归因排行不应为空")
	}
	for _, row := range rows {
		if row.Total() == 0 {
			t.Errorf("排行里出现了空行 %s", row.Extension)
		}
	}
	samples := attribution.Samples(scanner, 5, 10)
	found := false
	for _, list := range samples {
		for _, entryPath := range list {
			if strings.HasSuffix(entryPath, "dead.ani") {
				found = true
			}
			if strings.HasSuffix(entryPath, "hidden.ani") {
				t.Errorf("已解释的 %s 不该出现在无人引用样例里", entryPath)
			}
		}
	}
	if !found {
		t.Errorf("样例里应能看到 dead.ani, got %v", samples)
	}
}

// TestLiteralBoundsConsumesWholeSpec is the regression for a matcher that
// silently matched nothing: `%s` must be one span, not a `%` plus a stray `s`.
func TestLiteralBoundsConsumesWholeSpec(t *testing.T) {
	prefix, suffix := literalBounds("misc/templ%s.ani")
	if prefix != "misc/templ" || suffix != ".ani" {
		t.Fatalf("`%%s` 应整段吃掉, got prefix=%q suffix=%q", prefix, suffix)
	}
	if gotPrefix, _ := literalBounds("a/%02d/%s_b.img"); gotPrefix != "a/" {
		t.Errorf("前缀应停在第一个通配段之前, got %q want \"a/\"", gotPrefix)
	}
	if _, gotSuffix := literalBounds("a/%02d/%s_b.img"); gotSuffix != "_b.img" {
		t.Errorf("后缀应从最后一个通配段之后开始, got %q want \"_b.img\"", gotSuffix)
	}
	if !globEqual("misc/templ%s.ani", "misc/templateme.ani") {
		t.Error("globEqual 应能匹配单个通配段")
	}
	if globEqual("misc/templ%s.ani", "misc/deep/templateme.ani") {
		t.Error("通配段不得跨目录，否则会过度归因")
	}
	if !globEqual("%s", "anything.else.at.all") {
		t.Error("退化模板按定义匹配一切；正因为它有效，上层才必须剔除它")
	}
	if len(literalRunes("%s")) >= 3 || len(literalRunes("templ%s.ani")) < 3 {
		t.Errorf("字面量长度阈值失效: %q / %q", literalRunes("%s"), literalRunes("templ%s.ani"))
	}
}
