package services

import (
	"os"
	"reflect"
	"testing"
	"unicode/utf16"

	"pvfine/internal/pvf"
)

func TestSkillTreeMixedCoordinatesAndEdges(t *testing.T) {
	doc := parseSkillTree("[character job]\n`[swordman]` `none`\n[skill info]\n[index]\n5\n[cell pos]\n2 3\n[next skill]\n7\n[next skill]\n8\n[/skill info]\n[skill info]\n[index]\n7\n[icon pos]\n23 67\n[/skill info]\n[/character job]\n[character job]\n`[swordman]` `weaponmaster`\n[skill info]\n[index]\n8\n[cell pos]\n0 0\n[/skill info]\n[/character job]")
	if len(doc.Branches) != 2 || len(doc.Branches[0].Nodes) != 2 || len(doc.Branches[1].Nodes) != 1 {
		t.Fatalf("分支解析错误：%+v", doc)
	}
	n := doc.Branches[0].Nodes[0]
	if n.X != 94 || n.Y != 201 || !reflect.DeepEqual(n.Next, []int{7, 8}) {
		t.Fatalf("坐标或顺序错误：%+v", n)
	}
	half := doc.Branches[0].Nodes[1]
	if half.X != 23 || half.Y != 67 || half.PositionKind != "icon pos" {
		t.Fatalf("半格坐标错误：%+v", half)
	}
}

func TestCommonSkillsJobExclusions(t *testing.T) {
	text := "[common skill]\n169 190 197\n[/common skill]\n[except skill info]\n[info]\n`[creator mage]` 197\n[/info]\n[info]\n`[swordman]` 169 190\n[/info]\n[info]\n`[creator mage]` 190\n[/info]\n[/except skill info]"
	if got := parseCommonSkillIDs(text, "[creator mage]"); !reflect.DeepEqual(got, []int{169}) {
		t.Fatal(got)
	}
	if got := parseCommonSkillIDs(text, "[swordman]"); !reflect.DeepEqual(got, []int{197}) {
		t.Fatal(got)
	}
	if got := parseCommonSkillIDs(text, "[mage]"); !reflect.DeepEqual(got, []int{169, 190, 197}) {
		t.Fatal(got)
	}
}

func TestSkillTreeInvalidPositions(t *testing.T) {
	doc := parseSkillTree("[character job]\n`[mage]` `none`\n[skill info]\n[index]\n1\n[/skill info]\n[skill info]\n[index]\n2\n[cell pos]\n-1 2\n[/skill info]\n[/character job]")
	if len(doc.Issues) != 2 || len(doc.Branches[0].Nodes) != 0 {
		t.Fatalf("无效坐标未提示：%+v", doc)
	}
}

func TestSkillTreeReadJobScopedAndOverlay(t *testing.T) {
	a := pvf.New()
	defer a.Release()
	files := map[string]string{
		"clientonly/skillshoptreespindex.co": "[skill tree]\n`[swordman]` `SkillTree/Test.co` `[mage]` `SkillTree/Test.co`\n[/skill tree]",
		"clientonly/skilltree/test.co":       "[character job]\n`[swordman]` `none`\n[skill info]\n[index]\n169\n[cell pos]\n1 2\n[/skill info]\n[/character job]\n[character job]\n`[mage]` `none`\n[skill info]\n[index]\n169\n[icon pos]\n23 0\n[/skill info]\n[/character job]",
		"clientonly/commonskilllist.co":      "[common skill]\n169\n[/common skill]",
		"skill/skilllist.lst":                "0 `SwordmanSkill.lst` 3 `MageSkill.lst`",
		"skill/swordmanskill.lst":            "169 `Swordman/BackStep.skl`",
		"skill/mageskill.lst":                "169 `Mage/BackStep.skl`",
		"skill/swordman/backstep.skl":        "[name]\n`剑士后跳`\n[required level]\n0\n[explain]\n`向后跳跃\\n回避攻击，成功率100%%`",
		"skill/mage/backstep.skl":            "[name]\n`法师后跳`\n[explain]\n{8=`<1::description>`}",
		"list/n_string.lst":                  "1 `String/SkillTree.uv.str`",
	}
	for p, text := range files {
		if _, e := a.AddFileText(p, text, pvf.TypeScript); e != nil {
			t.Fatal(e)
		}
	}
	table := []byte{}
	for _, unit := range utf16.Encode([]rune("description>法师技能描述\\n第二行\r\n")) {
		table = append(table, byte(unit), byte(unit>>8))
	}
	a.AddFile("String/SkillTree.uv.str", table, pvf.TypeScript)
	s := NewFileGUIService(&core{archive: a})
	for job, name := range map[string]string{"[swordman]": "剑士后跳", "[mage]": "法师后跳"} {
		doc, e := s.ReadSkillTree(job, "sp")
		if e != nil {
			t.Fatal(e)
		}
		treeIndex, found := a.Find("clientonly/skilltree/test.co")
		if !found || doc.FileIndex != treeIndex || doc.Path != a.Path(treeIndex) {
			t.Fatalf("混合大小写注册路径应返回归档的实际文件身份：%d %s", doc.FileIndex, doc.Path)
		}
		if len(doc.Common) != 1 || doc.Common[0].Name != name || len(doc.Branches) != 1 || doc.Branches[0].Nodes[0].Name != name {
			t.Fatalf("技能必须按职业解析：%+v", doc)
		}
	}
	for job, want := range map[string]string{"[swordman]": "向后跳跃\n回避攻击，成功率100%", "[mage]": "法师技能描述\n第二行"} {
		doc, err := s.ReadSkillTree(job, "sp")
		if err != nil {
			t.Fatal(err)
		}
		if doc.Common[0].Explain != want || doc.Branches[0].Nodes[0].Explain != want {
			t.Fatalf("%s 描述应解析引用并保留换行：%q", job, doc.Common[0].Explain)
		}
	}
	swordman, err := s.ReadSkillTree("[swordman]", "sp")
	if err != nil {
		t.Fatal(err)
	}
	if swordman.Common[0].RequiredLevel == nil || *swordman.Common[0].RequiredLevel != 0 || swordman.Branches[0].Nodes[0].RequiredLevel == nil {
		t.Fatal("应保留等级0，不与缺失等级混淆")
	}
	mage, err := s.ReadSkillTree("[mage]", "sp")
	if err != nil {
		t.Fatal(err)
	}
	if mage.Common[0].RequiredLevel != nil {
		t.Fatal("缺失等级不应虚构为0")
	}
	if _, e := s.ReadSkillTree("[swordman]", "unknown"); e == nil {
		t.Fatal("不应读取未注册类型")
	}
	// Changes to skill names and learning levels must both follow the overlay.
	idx, _ := a.Find("skill/swordman/backstep.skl")
	if e := a.SetText(idx, "[name]\n`已更新`\n[required level]\n20\n[explain]\n`更新后的描述`"); e != nil {
		t.Fatal(e)
	}
	doc, e := s.ReadSkillTree("[swordman]", "sp")
	if e != nil {
		t.Fatal(e)
	}
	if doc.Common[0].Name != "已更新" || doc.Common[0].Explain != "更新后的描述" || doc.Common[0].RequiredLevel == nil || *doc.Common[0].RequiredLevel != 20 {
		t.Fatalf("未读取当前覆盖层：%s", doc.Common[0].Name)
	}
}

func TestSkillTreeReal110US(t *testing.T) {
	filename := os.Getenv("PVF_TESTFILE")
	if filename == "" {
		t.Skip("PVF_TESTFILE 未设置")
	}
	a, err := pvf.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if a.ClientVersion() != "110US" {
		t.Skip("使用 110US 真实样例验证")
	}
	s := NewFileGUIService(&core{archive: a})
	registrations, err := s.ListSkillTrees()
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 34 {
		t.Fatalf("注册数：%d", len(registrations))
	}
	for _, r := range registrations {
		t.Run(r.Job+"/"+r.Kind, func(t *testing.T) {
			doc, e := s.ReadSkillTree(r.Job, r.Kind)
			if e != nil {
				t.Fatal(e)
			}
			index, found := a.Find(r.Path)
			if !found || doc.FileIndex != index || doc.Path != a.Path(index) {
				t.Fatalf("注册路径 %s 应关联实际文件索引和路径：%d %s", r.Path, doc.FileIndex, doc.Path)
			}
			if len(doc.Branches) == 0 {
				t.Fatal("缺少转职分支")
			}
			if r.Job == "[creator mage]" {
				for _, n := range doc.Common {
					if n.ID == 197 {
						t.Fatal("缔造者应排除197")
					}
				}
			}
			for _, b := range doc.Branches {
				for _, n := range b.Nodes {
					if r.Job == "[swordman]" && r.Kind == "sp" && n.ID == 1 && (n.RequiredLevel == nil || *n.RequiredLevel != 5) {
						t.Errorf("格挡学习等级应为5：%v", n.RequiredLevel)
					}
					if n.FileIndex < 0 {
						t.Errorf("%s 技能 #%d 无法解析", b.Name, n.ID)
					}
				}
			}
			t.Logf("%d 分支，%d 通用技能，%d 条提示", len(doc.Branches), len(doc.Common), len(doc.Issues))
		})
	}
}
