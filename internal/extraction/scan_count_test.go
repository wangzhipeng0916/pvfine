package extraction

import (
	"os"
	"strings"
	"testing"

	"pvfine/internal/annotations"
	"pvfine/internal/pvf"
)

// TestCountPrefixedRecordBoundaries pins the record shape on a synthetic
// fixture. Unlike the retired line-grouped model, a count-prefixed stream is
// flat, so a fixture reproduces the real layout faithfully.
func TestCountPrefixedRecordBoundaries(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "map.specialPassiveObject", Extensions: []string{".map"}, Section: "special passive object",
		TokenTypes: []int32{0},
		Record: &Record{CountPrefixed: &CountPrefixed{
			PrefixTokens: 5, CountIndex: 4, ItemTokens: 6, KindIndex: 0,
			NestedKinds: []string{"hellparty"},
		}},
		Target: Target{Kind: TargetRegistry, Relation: "被动对象"},
	}}}
	f := newFixtureFrom(t, []entry{
		// 两条记录：第一条带 1 个 item（10 = door.obj），第二条 n=0（纯坐标，无 item）。
		{"map/forest/records.map", "[special passive object]\n" +
			"\t10\t1\t2\t0\t1\t`[item]`\t0\t1\t-1\t-1\t-1\n" +
			"\t11\t3\t4\t0\t0\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)
	result := f.scanFile(t, scanner, "map/forest/records.map")

	if len(result.Refs) != 2 {
		t.Fatalf("应切出 2 条记录引用, got %d: %+v", len(result.Refs), result.Refs)
	}
	want := []string{"10", "11"}
	for i, ref := range result.Refs {
		if ref.Value != want[i] {
			t.Errorf("第 %d 条记录 ID 应为 %s, got %s", i, want[i], ref.Value)
		}
		if ref.State != StateResolved {
			t.Errorf("记录 %s 应解析成功, got %s (%s)", ref.Value, ref.State, ref.Note)
		}
	}
	if result.Refs[0].TargetPath != "passiveobject/obj/door.obj" {
		t.Errorf("记录 10 应解析到 door.obj, got %s", result.Refs[0].TargetPath)
	}
	// item 载荷不得被当成引用：只有记录 ID 是声明位。
	for _, ref := range result.Refs {
		if ref.Value == "1" || ref.Value == "2" || ref.Value == "3" || ref.Value == "4" {
			t.Errorf("坐标/载荷值 %s 被误当成引用", ref.Value)
		}
	}
	for _, u := range result.Unmodeled {
		if u.Shape == "record-malformed" {
			t.Errorf("合法记录被报告为 malformed: %+v", u)
		}
	}
}

// TestCountPrefixedNestedBlockSkipsPayload: a paired item consumes its whole
// block, so the record after it must still be found.
func TestCountPrefixedNestedBlockSkipsPayload(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "map.specialPassiveObject", Extensions: []string{".map"}, Section: "special passive object",
		TokenTypes: []int32{0},
		Record: &Record{CountPrefixed: &CountPrefixed{
			PrefixTokens: 5, CountIndex: 4, ItemTokens: 6, KindIndex: 0,
			NestedKinds: []string{"hellparty"},
		}},
		Target: Target{Kind: TargetRegistry, Relation: "被动对象"},
	}}}
	f := newFixtureFrom(t, []entry{
		{"map/forest/nested.map", "[special passive object]\n" +
			"\t10\t1\t2\t0\t1\t`[hellparty]`\t125\t25\t1\t126\t25\t1\t`[/hellparty]`\n" +
			"\t11\t3\t4\t0\t0\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)
	result := f.scanFile(t, scanner, "map/forest/nested.map")

	if len(result.Refs) != 2 {
		t.Fatalf("配对块之后的记录必须仍被切出, got %d: %+v", len(result.Refs), result.Refs)
	}
	if result.Refs[0].Value != "10" || result.Refs[1].Value != "11" {
		t.Errorf("记录 ID 顺序错误: %s, %s", result.Refs[0].Value, result.Refs[1].Value)
	}
	if result.Refs[0].State != StateResolved {
		t.Errorf("带配对块的记录应照常解析, got %s (%s)", result.Refs[0].State, result.Refs[0].Note)
	}
	// 配对块内部值必须进候选队列，而不是消失。
	nested := 0
	for _, u := range result.Unmodeled {
		if strings.HasPrefix(u.Shape, "nested[hellparty]") {
			nested++
		}
	}
	if nested == 0 {
		t.Error("配对块内部值未进入候选队列（会退化成静默丢弃）")
	}
}

// TestCountPrefixedConsumesRealMapsExactly is the strongest evidence available
// for the record rule: over every real .map carrying the section, the walk must
// land exactly on the last token. Measured on 86: 5467 files / 17996 records.
func TestCountPrefixedConsumesRealMapsExactly(t *testing.T) {
	file := os.Getenv("PVF_TESTFILE")
	if file == "" {
		t.Skip("PVF_TESTFILE 未设置")
	}
	archive, err := pvf.Open(file)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	catalog, err := LoadCatalog(EmbeddedEdges)
	if err != nil {
		t.Fatalf("加载边目录失败: %v", err)
	}
	scanner := NewScanner(archive, catalog, NewRegistrySet(archive, map[string]annotations.RelationSpec{}))

	files, exact, records, malformedTotal := 0, 0, 0, 0
	failures := []string{}
	for i := int32(0); i < archive.FileCount(); i++ {
		if !strings.HasSuffix(strings.ToLower(archive.FullPath(i)), ".map") {
			continue
		}
		text, err := archive.Text(i)
		if err != nil {
			continue
		}
		view := pvf.ParseScriptView(text)
		ords := []int{}
		for j := range view.Elements {
			el := &view.Elements[j]
			if el.Kind != pvf.ScriptElementToken {
				continue
			}
			if strings.EqualFold(el.Section, "special passive object") {
				ords = append(ords, j)
			}
		}
		if len(ords) == 0 {
			continue
		}
		files++
		var result ScanResult
		got, consumed := scanner.walkCountPrefixed(&view, ords, ".map", &result)
		for _, u := range result.Unmodeled {
			if u.Shape == "record-malformed" {
				malformedTotal++
			}
		}
		records += got
		if consumed == len(ords) {
			exact++
			continue
		}
		if len(failures) < 5 {
			failures = append(failures, archive.FullPath(i))
		}
	}
	if files == 0 {
		t.Skip("该归档没有含 [special passive object] 的 .map")
	}
	t.Logf("含该段的 .map=%d 精确消费=%d 记录=%d malformed=%d", files, exact, records, malformedTotal)
	if exact != files {
		t.Errorf("有 %d 个文件未被精确消费（样例 %v）", files-exact, failures)
	}
	if malformedTotal != 0 {
		t.Errorf("合法归档上不应出现 record-malformed: %d 条", malformedTotal)
	}
	if records < 1000 {
		t.Errorf("记录数异常偏少: %d（86 实测约 17996）", records)
	}
}
