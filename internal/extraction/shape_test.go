package extraction

import (
	"testing"

	"pvfine/internal/pvf"
)

func TestFileReferenceShapes(t *testing.T) {
	cases := []struct {
		value   string
		fileRef bool
		asset   bool
		archive bool
	}{
		{"Animation/Bottom.ani", true, false, true},
		{"door.ani", true, false, true}, // 无目录的 owner-relative，回归用例
		{"Event.ai", true, false, true}, // .aic [ai pattern] 的实际形状
		{"../../../Map/GrakKarak/Animation/StonePillar0.ani", true, false, true},
		{"Town/SilverCrown/SilverCrown_MainStreet.map", true, false, true},
		{"Map/04far0.img", true, true, false},
		{"item/new_equipment/08_necklace/necklace.img", true, true, false},
		{"M_LUTEON", false, false, false},                      // 声音名，无扩展名
		{"[normal]", false, false, false},                      // 类型标签
		{"*", false, false, false},                             // 通配但非路径
		{"AABBNNNNFFMM\r\n BBNNHHOOAAKK", false, false, false}, // 多行数据块
		{"Sprite/Monster/%d%d.img", false, true, false},        // 动态资产，非静态 fileRef
		{"Sprite/Monster/%d%d.ani", false, false, false},
		{"", false, false, false},
		{"Tool/Bin", false, false, false}, // 目录形但无扩展名
	}
	for _, tc := range cases {
		if got := looksLikeFileRef(tc.value); got != tc.fileRef {
			t.Errorf("looksLikeFileRef(%q) = %v, want %v", tc.value, got, tc.fileRef)
		}
		if got := looksLikeAssetPath(tc.value); got != tc.asset {
			t.Errorf("looksLikeAssetPath(%q) = %v, want %v", tc.value, got, tc.asset)
		}
		if got := looksLikeArchiveEntry(tc.value); got != tc.archive {
			t.Errorf("looksLikeArchiveEntry(%q) = %v, want %v", tc.value, got, tc.archive)
		}
	}
	if !isDynamicPath("Sprite/Monster/%d%d.img") {
		t.Error("isDynamicPath 未识别百分号分卷模板")
	}
	if isDynamicPath("Animation/Bottom.ani") {
		t.Error("isDynamicPath 误判静态路径")
	}
}

func TestCandidateShape(t *testing.T) {
	cases := []struct {
		name  string
		el    pvf.ScriptElement
		shape string
	}{
		{"反引号路径", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 6, Value: "Animation/x.ani"}, "path"},
		{"反引号资产", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 6, Value: "Map/04far0.img"}, "path"},
		{"动态资产", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 6, Value: "Monster/*.img"}, "path"},
		{"声音名不算引用", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 6, Value: "M_LUTEON"}, ""},
		{"四位以上数字", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 0, Value: "48390"}, "numeric"},
		{"小整数不算", pvf.ScriptElement{Kind: pvf.ScriptElementToken, TokenType: 0, Value: "5"}, ""},
		{"section 元素不算", pvf.ScriptElement{Kind: pvf.ScriptElementSection, TokenType: 3, Value: "tile"}, ""},
	}
	for _, tc := range cases {
		if got := candidateShape(&tc.el); got != tc.shape {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.shape)
		}
	}
}

func TestNormalizeTag(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"`[item]`", "item"},
		{"[ITEM]", "item"},
		{" item ", "item"},
		{"`[passive object]`", "passive object"},
		{"trap", "trap"},
	} {
		if got := normalizeTag(tc.in); got != tc.want {
			t.Errorf("normalizeTag(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("AABB\r\n BBNN"); got != "AABB~" {
		t.Errorf("clip 多行截断错误: %q", got)
	}
	if got := clip("short"); got != "short" {
		t.Errorf("clip 短串被改动: %q", got)
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	if got := clip(string(long)); len(got) != 72 {
		t.Errorf("clip 长度=%d want 72", len(got))
	}
}

func TestRefFollowable(t *testing.T) {
	resolved := Ref{State: StateResolved, Target: 7, Kind: TargetEntry}
	if !resolved.Followable() {
		t.Error("已解析条目引用应可跟随")
	}
	asset := Ref{State: StateResolved, Target: 7, Kind: TargetAsset}
	if asset.Followable() {
		t.Error("资产引用不得跟随（归档内无该资源）")
	}
	missing := Ref{State: StateMissing, Target: 7, Kind: TargetRegistry}
	if missing.Followable() {
		t.Error("未解析引用不得跟随")
	}
}
