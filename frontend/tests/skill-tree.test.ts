import { describe, expect, it } from "vitest";
import { skillTreeEdge, groupSkillTreeCells, skillTreeRowLevels } from "../src/gui/skillTree";
import type { SkillTreeNode } from "../bindings/pvfine/services/models";
const node = (x: number, y: number): SkillTreeNode => ({id: 1, x, y, name: "测试", explain: "", positionKind: "icon pos", position: [x, y], next: [], path: "", fileIndex: -1, icon: null, requiredLevel: null});
describe("技能树连线", () => {
  it("横向连线停在目标图标前，反向关系也保留箭头方向", () => {
    expect(skillTreeEdge(node(0, 0), node(94, 0))).toBe("M 34 16 H 62 V 16 H 90");
    expect(skillTreeEdge(node(94, 0), node(0, 0))).toBe("M 92 16 H 64 V 16 H 36");
  });
  it("纵向跨行连接保留像素偏移，并绕开图标中心", () => {
    expect(skillTreeEdge(node(23, 0), node(47, 134))).toBe("M 39 40 V 85.5 H 63 V 131");
  });
});

 describe("同位置技能", () => {
  it("保留坐标和文件顺序，同格技能可分别选择", () => {
    const a = {...node(235, 938), id: 319};
    const b = {...node(235, 938), id: 112};
    const c = {...node(329, 938), id: 113};
    const cells = groupSkillTreeCells([a, c, b]);
    expect(cells.map(cell => cell.nodes.map(n => n.id))).toEqual([[319, 112], [113]]);
    expect(cells[0]!.nodes[1]).toBe(b);
    expect([b.x, b.y]).toEqual([235, 938]);
  });
});

 describe("技能树行等级", () => {
  it("SP 按最左侧技能取学习等级，同坐标按文件顺序，不随所选技能变化", () => {
    const nodes = [
      {...node(94, 0), requiredLevel: 20},
      {...node(23, 0), requiredLevel: 5},
      {...node(23, 0), requiredLevel: 10},
      {...node(0, 134), requiredLevel: 0},
      {...node(0, 201), requiredLevel: null},
    ];
    const levels = skillTreeRowLevels(nodes, "sp");
    expect(levels.get(0)).toBe(5);
    expect(levels.has(1)).toBe(false);
    expect(levels.get(2)).toBe(0);
    expect(levels.get(3)).toBeNull();
  });
  it("TP 不产生等级标注", () => {
    expect(skillTreeRowLevels([{...node(0, 0), requiredLevel: 50}], "tp").size).toBe(0);
  });
});
