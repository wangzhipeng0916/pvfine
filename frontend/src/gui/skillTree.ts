import type { SkillTreeNode } from "../../bindings/pvfine/services/models";

export const skillBranchNames: Record<string, string> = {
  none: "未转职", weaponmaster: "剑魂", soulbringer: "鬼泣", berserker: "狂战士", asura: "阿修罗", swordghost: "剑影",
  ranger: "漫游枪手", launcher: "枪炮师", mechanic: "机械师", spitfire: "弹药专家", assault: "合金战士",
  nenmaster: "气功师", striker: "散打", streetfighter: "街霸", grappler: "柔道家",
  elementalmaster: "元素师", summoner: "召唤师", battlemage: "战斗法师", witch: "魔道学者", enchantress: "小魔女",
  crusader: "圣骑士", infighter: "蓝拳圣使", exorcist: "驱魔师", avenger: "复仇者", inquisitor: "异端审判者", shaman: "巫女", mistress: "诱魔者",
  rogue: "刺客", necromancer: "死灵术士", kunoichi: "忍者", shadowdancer: "影舞者",
  muse: "缪斯", traveler: "旅人", hunter: "猎人", vigilante: "妖护使",
  elementalbomber: "元素爆破师", glacialmaster: "冰结师", bloodmage: "血法师", swiftmaster: "逐风者", dimensionwalker: "次元行者",
  miko: "巫女", temptress: "诱魔者", demonslayer: "契魔者", vegabond: "流浪武士", blade: "刃影",
  duelist: "决战者", vanguard: "征战者", dragonianlancer: "狩猎者", darklancer: "暗枪士",
  hitman: "暗刃", agent: "特工", troubleshooter: "战线佣兵", specialist: "源能专家",
  elvenknight: "精灵骑士", chaos: "混沌魔灵", paladin: "帕拉丁", dragonknight: "龙骑士",
  swordmaster: "驭剑士", darktempler: "暗殿骑士", demonicslayer: "契魔者", vagabond: "流浪武士", spectre: "刃影",
};

// Use the edge facing the destination, keeping arrows outside the 32px icon.
export function skillTreeEdge(source: SkillTreeNode, target: SkillTreeNode): string {
  const sx = source.x + 16, sy = source.y + 16;
  const tx = target.x + 16, ty = target.y + 16;
  if (Math.abs(tx - sx) > Math.abs(ty - sy)) {
    const direction = Math.sign(tx - sx);
    const start = sx + direction * 18, end = tx - direction * 20;
    const mid = (start + end) / 2;
    return `M ${start} ${sy} H ${mid} V ${ty} H ${end}`;
  }
  const direction = Math.sign(ty - sy) || 1;
  const start = sy + direction * 24, end = ty - direction * 19;
  const mid = (start + end) / 2;
  return `M ${sx} ${start} V ${mid} H ${tx} V ${end}`;
}

export interface SkillTreeCell {
  key: string;
  nodes: SkillTreeNode[];
}

/** Keep source order and coordinates; overlapping skills remain individually selectable. */
export function groupSkillTreeCells(nodes: SkillTreeNode[]): SkillTreeCell[] {
  const cells = new Map<string, SkillTreeCell>();
  for (const node of nodes) {
    const key = `${node.x}:${node.y}`;
    let cell = cells.get(key);
    if (!cell) { cell = {key, nodes: []}; cells.set(key, cell); }
    cell.nodes.push(node);
  }
  return [...cells.values()];
}

/** Row labels use the leftmost skill, with source order breaking position ties. */
export function skillTreeRowLevels(nodes: SkillTreeNode[], kind: string): Map<number, number | null> {
  const first = new Map<number, SkillTreeNode>();
  if (kind !== "sp") return new Map();
  for (const node of nodes) {
    const row = Math.floor(node.y / 67);
    const previous = first.get(row);
    if (!previous || node.x < previous.x) first.set(row, node);
  }
  return new Map([...first].map(([row, node]) => [row, node.requiredLevel]));
}
