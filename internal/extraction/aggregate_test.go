package extraction

import "testing"

// TestAggregatorSplitsUnknownReasons guards the vocabulary the edge report is
// read with. The column used to be labelled 歧义 while carrying every Unknown,
// so a template-heavy asset edge looked like a same-name conflict and an
// expectExt-too-narrow edge looked like either one. The three causes drive
// opposite decisions: 同名 needs roots or a narrower value rule, 动态模板 is
// correct-as-is, and 其他 (mostly "解析到条目但扩展名不在 expectExt 内") means the
// declaration is too tight — which is exactly how #40 found obj.stringData.
func TestAggregatorSplitsUnknownReasons(t *testing.T) {
	aggregate := NewAggregator()
	refs := []Ref{
		{EdgeID: "map.thing", Value: "a.ani", State: StateResolved},
		{EdgeID: "map.thing", Value: "b.ani", State: StateMissing},
		{EdgeID: "map.thing", Value: "c.img", State: StateUnknown,
			Note: "解析到条目但扩展名不在 expectExt 内，边声明可能有误"},
		{EdgeID: "map.thing", Value: "d.ani", State: StateUnknown,
			Note: "同名条目 3 个，无法判定目标"},
		{EdgeID: "map.thing", Value: "%s/e.ani", State: StateUnknown,
			Note: "动态路径，无法静态判定"},
		{EdgeID: "map.thing", Value: "%02d/f.img", State: StateUnknown,
			Note: "动态路径，无法静态判定"},
	}
	aggregate.AddScan(ScanResult{Path: "x/y.map", Refs: refs, Vocabulary: 6})

	rows := aggregate.EdgeStats()
	if len(rows) != 1 {
		t.Fatalf("应汇总出 1 条边, got %d", len(rows))
	}
	row := rows[0]
	if row.Values != 6 || row.Resolved != 1 || row.Missing != 1 || row.Unknown != 4 {
		t.Errorf("三态计数错: %+v", row)
	}
	if row.Ambiguous != 1 {
		t.Errorf("同名歧义应为 1, got %d", row.Ambiguous)
	}
	if row.Dynamic != 2 {
		t.Errorf("动态模板应为 2, got %d", row.Dynamic)
	}
	// 关键不变式：拆出来的两档不得超过 Unknown 总数，剩下的差额就是"其他"，
	// 报表必须把它显式留着——否则扩展名过窄这类信号会被两档吸收掉。
	if row.Ambiguous+row.Dynamic > row.Unknown {
		t.Errorf("拆分超出未知总数: %d+%d > %d", row.Ambiguous, row.Dynamic, row.Unknown)
	}
	if other := row.Unknown - row.Ambiguous - row.Dynamic; other != 1 {
		t.Errorf("其他（声明过窄）应为 1, got %d", other)
	}
}

// TestUnknownReasonOnlyCountsRecognisedNotes pins the negative case: a note in
// neither recognised family must not leak into either split column.
func TestUnknownReasonOnlyCountsRecognisedNotes(t *testing.T) {
	aggregate := NewAggregator()
	aggregate.AddScan(ScanResult{Path: "x/y.map", Refs: []Ref{
		{EdgeID: "map.thing", Value: "z", State: StateUnknown, Note: "表文件本身不跟随"},
	}})
	row := aggregate.EdgeStats()[0]
	if row.Unknown != 1 || row.Ambiguous != 0 || row.Dynamic != 0 {
		t.Errorf("未识别成因不该进任何拆档: %+v", row)
	}
}
