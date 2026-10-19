package extraction

import (
	"strings"
	"testing"
)

// These tests pin the two silent-loss paths found in the self-review. Both had
// the same failure signature: a reference that resolved to nothing at all, so
// no report, no gap queue and no way to notice the catalog was wrong. A
// real-archive single-dungeon run never caught either one.

// TestExpectExtMismatchStaysVisible is the S2-1 regression: a value that
// resolves to a real entry but violates the edge's expectExt must be reported
// as unknown, not dropped. Dropping it makes a mis-declared edge invisible
// forever, because the gap queue only sees tokens with no edge at all.
func TestExpectExtMismatchStaysVisible(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "obj.mysteryImage", Extensions: []string{".obj"}, Section: "mystery image",
		TokenTypes: []int32{6},
		Target:     Target{Kind: TargetEntry, ExpectExt: []string{".til"}},
	}}}
	f := newFixtureFrom(t, []entry{
		{"passiveobject/obj/mismatch.obj", "[mystery image]\n\t`mat.qu`\n"},
		{"passiveobject/obj/mat.qu", "[dummy]\n\t1\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)

	refs := f.scanNode(t, scanner, "passiveobject/obj/mismatch.obj")
	if len(refs) != 1 {
		t.Fatalf("扩展名不符也必须产出引用, got %d: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.State != StateUnknown {
		t.Fatalf("期望 unknown, got %s (%s)", ref.State, ref.Note)
	}
	if ref.TargetPath != "passiveobject/obj/mat.qu" {
		t.Errorf("应保留实际解析到的目标, got %q", ref.TargetPath)
	}
	if !strings.Contains(ref.Note, "expectExt") {
		t.Errorf("说明应指向边声明本身, got %q", ref.Note)
	}
}

// TestBareNameResolvesToNearestSibling covers D1: an .aic lists `[ai pattern]`
// and `[key stream]` as bare file names while the entries live in a sibling
// sub-directory. Owner-relative, `(r)` and root lookup all miss, so the
// basename index must pick the candidate sharing the longest directory prefix.
func TestBareNameResolvesToNearestSibling(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "aic.keyStream", Extensions: []string{".aic"}, Section: "key stream",
		TokenTypes: []int32{6},
		Target:     Target{Kind: TargetEntry, ExpectExt: []string{".key"}},
	}}}
	f := newFixtureFrom(t, []entry{
		{"script/swordman/kagemaru.aic", "[key stream]\n\t`1.key`\n"},
		{"script/swordman/key/1.key", "[frame]\n\t1\n"},
		{"elsewhere/deep/unrelated/1.key", "[frame]\n\t2\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)

	refs := f.scanNode(t, scanner, "script/swordman/kagemaru.aic")
	if len(refs) != 1 {
		t.Fatalf("期望 1 条引用, got %d: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.State != StateResolved {
		t.Fatalf("裸文件名应经 basename 索引解析成功, got %s (%s)", ref.State, ref.Note)
	}
	if ref.TargetPath != "script/swordman/key/1.key" {
		t.Errorf("应选中目录前缀最近的同名条目, got %s", ref.TargetPath)
	}
}

// TestAmbiguousBareNameIsUnknown states the safety half of D1: when two same
// named entries are equally near, guessing silently corrupts the graph. The
// reference must be reported as unknown with the candidate count.
func TestAmbiguousBareNameIsUnknown(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "aic.keyStream", Extensions: []string{".aic"}, Section: "key stream",
		TokenTypes: []int32{6},
		Target:     Target{Kind: TargetEntry, ExpectExt: []string{".key"}},
	}}}
	f := newFixtureFrom(t, []entry{
		{"script/swordman/kagemaru.aic", "[key stream]\n\t`1.key`\n"},
		{"script/groupA/1.key", "[frame]\n\t1\n"},
		{"script/groupB/1.key", "[frame]\n\t2\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)

	refs := f.scanNode(t, scanner, "script/swordman/kagemaru.aic")
	if len(refs) != 1 {
		t.Fatalf("期望 1 条引用, got %d: %+v", len(refs), refs)
	}
	if refs[0].State != StateUnknown {
		t.Fatalf("等距同名条目不得猜测, got %s (%s)", refs[0].State, refs[0].Note)
	}
	if !strings.Contains(refs[0].Note, "2") {
		t.Errorf("说明应给出候选个数, got %q", refs[0].Note)
	}
}

// TestUnknownKindTagStaysVisible lives in real_archive_test.go instead: the
// synthetic writer flattens a section body onto a single line, so the
// line-grouped kind-route path can only be exercised on real data.

// TestClosureCapReportsWhatItDropped pins the third silent-loss shape: a
// MaxEntries walk stops early but returns a perfectly well-formed closure, so
// nothing tells the caller the set is a sample. A cap someone can hit without it
// showing up is not a guard rail, it is a wrong answer with better manners.
func TestClosureCapReportsWhatItDropped(t *testing.T) {
	f := newFixture(t)
	scanner := f.scanner(t)
	seed, ok := f.archive.Find("map/forest/a.map")
	if !ok {
		t.Fatal("fixture 缺少种子")
	}
	full, err := scanner.Closure([]int32{seed}, Options{})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	if full.Truncated || full.Dropped != 0 {
		t.Errorf("未设上限不该标记截断: truncated=%v dropped=%d", full.Truncated, full.Dropped)
	}
	if len(full.Nodes) < 5 {
		t.Fatalf("fixture 闭包应有 5 条以上条目, got %d", len(full.Nodes))
	}

	capped, err := scanner.Closure([]int32{seed}, Options{MaxEntries: 2})
	if err != nil {
		t.Fatalf("Closure(cap): %v", err)
	}
	if !capped.Truncated {
		t.Errorf("命中上限必须可见: nodes=%d", len(capped.Nodes))
	}
	if len(capped.Nodes) != 2 || capped.Dropped < 1 {
		t.Errorf("上限应停在 2 条并报告未展开数, got nodes=%d dropped=%d", len(capped.Nodes), capped.Dropped)
	}
	if len(capped.Nodes)+capped.Dropped > len(full.Nodes) {
		t.Errorf("截断不能凭空造出可达条目: capped=%d+%d full=%d",
			len(capped.Nodes), capped.Dropped, len(full.Nodes))
	}
}

// TestCountPrefixedEntryKeepsOwnerDirectory pins the asymmetry that made a
// reference resolve *before* it was declared and stop resolving after: the plain
// eval path passes the referrer into resolveRelative, while the count-prefixed
// record path passed "". Same value, two answers, is not a rule anyone can
// reason about — and it silently punishes exactly the edges worth declaring.
func TestCountPrefixedEntryKeepsOwnerDirectory(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "map.actionList", Extensions: []string{".map"}, Section: "action list",
		TokenTypes: []int32{0, 6},
		Record:     &Record{CountPrefixed: &CountPrefixed{PrefixTokens: 2, CountIndex: 1, ItemTokens: 2}},
		Target:     Target{Kind: TargetEntry, ExpectExt: []string{".act"}},
	}}}
	f := newFixtureFrom(t, []entry{
		{"map/forest/list.map", "[action list]\n\t`sub/a.act`\t1\t7\t`sub/b.act`\n\t`sub/c.act`\t0\n"},
		{"map/forest/sub/a.act", "[dummy]\n\t1\n"},
		{"map/forest/sub/c.act", "[dummy]\n\t2\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)
	refs := f.scanNode(t, scanner, "map/forest/list.map")
	if len(refs) != 2 {
		t.Fatalf("应切出 2 条记录引用（条目载荷不算）, got %d: %+v", len(refs), refs)
	}
	want := map[string]string{"sub/a.act": "map/forest/sub/a.act", "sub/c.act": "map/forest/sub/c.act"}
	for _, ref := range refs {
		target, ok := want[ref.Value]
		if !ok {
			t.Errorf("多出一条引用 %q", ref.Value)
			continue
		}
		if ref.State != StateResolved {
			t.Errorf("%s 应按 owner-relative 解析, got %s (%s)", ref.Value, ref.State, ref.Note)
			continue
		}
		if ref.TargetPath != target {
			t.Errorf("%s 解析到 %s, 期望 %s", ref.Value, ref.TargetPath, target)
		}
	}
}

// TestDeclaredRootUnlocksClassRelativePaths is the `target.roots` rung: PVF
// sometimes writes a value relative to the *target class* root, so
// `town/alfhlyra.twn` holds `Alfhlyra/Alfhlyra.map` for the entry at
// `map/alfhlyra/alfhlyra.map`. Owner-relative and archive-root both miss it,
// which is how two shipped edges measured 0.000 until this rung existed. The
// second section proves the resolution comes from the declaration, not from a
// lucky guess: identical value, no roots, stays missing.
func TestDeclaredRootUnlocksClassRelativePaths(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{
		{ID: "twn.area", Extensions: []string{".twn"}, Section: "area", TokenTypes: []int32{6},
			Target: Target{Kind: TargetEntry, ExpectExt: []string{".map"}, Roots: []string{"map"}}},
		{ID: "twn.noRoot", Extensions: []string{".twn"}, Section: "no root", TokenTypes: []int32{6},
			Target: Target{Kind: TargetEntry, ExpectExt: []string{".map"}}},
	}}
	f := newFixtureFrom(t, []entry{
		{"town/alfhlyra.twn", "[area]\n\t`Alfhlyra/Alfhlyra.map`\n\t`Alfhlyra/Nowhere.map`\n" +
			"[no root]\n\t`Alfhlyra/Alfhlyra.map`\n"},
		{"map/alfhlyra/alfhlyra.map", "[tile]\n\t`tiles/ground.til`\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)
	refs := f.scanNode(t, scanner, "town/alfhlyra.twn")
	if len(refs) != 3 {
		t.Fatalf("期望 3 条引用, got %d: %+v", len(refs), refs)
	}
	var areaHit, areaMiss, noRoot *Ref
	for at := range refs {
		switch {
		case refs[at].EdgeID == "twn.area" && refs[at].Value == "Alfhlyra/Alfhlyra.map":
			areaHit = &refs[at]
		case refs[at].EdgeID == "twn.area":
			areaMiss = &refs[at]
		default:
			noRoot = &refs[at]
		}
	}
	if areaHit == nil || areaHit.State != StateResolved ||
		areaHit.TargetPath != "map/alfhlyra/alfhlyra.map" {
		t.Errorf("声明的 map/ 根应把值解到真条目, got %+v", areaHit)
	}
	if areaMiss == nil || areaMiss.State != StateMissing {
		t.Errorf("目标确实不存在时应判 missing, got %+v", areaMiss)
	}
	if noRoot == nil || noRoot.State != StateMissing {
		t.Errorf("未声明 roots 的同值必须仍解不出（否则这条档就不是原因）, got %+v", noRoot)
	}
}

// TestRelativeRootResolvesAgainstOwnerParent covers the other half of the roots
// knob: a `../` root is relative to the referrer, not to the archive. `.key`
// needs it because its own value already carries the `key/` directory it lives
// in, so owner-relative lands one level too deep and only the parent matches.
// Measured on 86: 772/773 with the parent, 0/773 with owner or archive root.
func TestRelativeRootResolvesAgainstOwnerParent(t *testing.T) {
	doc := Document{Version: 1, Edges: []Edge{{
		ID: "key.particleFilename", Extensions: []string{".key"}, Section: "particle filename",
		TokenTypes: []int32{6},
		Target:     Target{Kind: TargetEntry, ExpectExt: []string{".ptl"}, Roots: []string{"../"}},
	}}}
	f := newFixtureFrom(t, []entry{
		{"aicharacter/hero/key/damage.key", "[particle filename]\n\t`key/dummy.ptl`\n"},
		{"aicharacter/hero/key/dummy.ptl", "[dummy]\n\t1\n"},
	}, doc)
	scanner := f.scannerWith(t, doc)
	refs := f.scanNode(t, scanner, "aicharacter/hero/key/damage.key")
	if len(refs) != 1 {
		t.Fatalf("期望 1 条引用, got %d: %+v", len(refs), refs)
	}
	if refs[0].State != StateResolved {
		t.Fatalf("相对 owner 父目录应解出，got %s (%s)", refs[0].State, refs[0].Note)
	}
	if refs[0].TargetPath != "aicharacter/hero/key/dummy.ptl" {
		t.Errorf("目标 = %s, 期望 aicharacter/hero/key/dummy.ptl", refs[0].TargetPath)
	}
}
