import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createPinia, disposePinia, setActivePinia, type Pinia } from "pinia";
import { nextTick } from "vue";

const mocks = vi.hoisted(() => ({
  Prepare: vi.fn(),
  Release: vi.fn(),
  ExecuteDialog: vi.fn(),
  Categories: vi.fn(),
  Presets: vi.fn(),
  Relations: vi.fn(),
  events: new Map<string, () => void>(),
}));
vi.mock("../bindings/pvfine/services", () => ({ ExtractionService: mocks }));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: (name: string, handler: () => void) => mocks.events.set(name, handler) },
}));

import {
  bytesLabel,
  coverageLabel,
  parseExtraSeeds,
  relationLabel,
  stateTotal,
  unresolvedCount,
  useExtractionStore,
} from "../src/stores/extraction";
import type { ExtractionPreview } from "../bindings/pvfine/services/models";

const categories = [
  { id: "scene", name: "场景与副本", files: 1, bytes: 512, refs: 4, autoFiles: 0 },
  { id: "mob", name: "怪物", files: 20, bytes: 2048, refs: 60, autoFiles: 3 },
  { id: "ani", name: "动画", files: 160, bytes: 40960, refs: 500, autoFiles: 12 },
];

function preview(overrides: Partial<ExtractionPreview> = {}): ExtractionPreview {
  return {
    id: "plan-1",
    categories,
    closureCount: 181,
    mainCount: 181,
    ancestorCount: 0,
    externalCount: 0,
    danglingCount: 0,
    exportedCount: 181,
    states: { resolved: 900, missing: 80, unknown: 15 },
    vocabCoverage: 0.55,
    assetTotal: 62,
    assetNpkMatched: 50,
    assetDynamic: 12,
    assetContainers: ["sprite_character_swordman_effect.NPK"],
    assetNpkBytes: 105_600_000,
    strict: false,
    layout: "mirror",
    ...overrides,
  };
}

let pinia: Pinia;
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
  vi.clearAllMocks();
  mocks.events.clear();
  mocks.Prepare.mockResolvedValue(preview());
  mocks.Release.mockResolvedValue(undefined);
  mocks.ExecuteDialog.mockResolvedValue({ dir: "D:/out/pvf", files: 181, bytes: 43520, autoCompleted: 0, dangling: 0 });
  mocks.Presets.mockResolvedValue([
    { id: "dungeon", name: "副本", description: "副本", seedHint: "dungeon/x.dgn", categories: [] },
    { id: "equipment", name: "装备", description: "装备", seedHint: "装备:ID", categories: ["item", "attach", "ani"] },
  ]);
  mocks.Relations.mockResolvedValue([
    { name: "任务", rows: -1, listPath: "" },
    { name: "装备", rows: 119532, listPath: "equipment/equipment.lst" },
  ]);
});
afterEach(() => {
  useExtractionStore().close();
  disposePinia(pinia);
});

async function settle() {
  await nextTick();
  await useExtractionStore().refresh();
  await nextTick();
}

function lastRequest() {
  const calls = mocks.Prepare.mock.calls;
  return calls[calls.length - 1]?.[0];
}

test("open 用当前勾选状态准备预览，默认全类别、文本形态、原路径镜像", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "提取 a.map 的依赖");
  await settle();
  expect(store.visible).toBe(true);
  expect(store.preview?.exportedCount).toBe(181);
  const request = lastRequest();
  expect(request.seeds).toEqual(["map/forest/a.map"]);
  // 空勾选就是"全部类别"，不能把它翻译成显式全选，否则语义上等价但请求体不同。
  expect(request.categories).toBeUndefined();
  expect(request.form).toBe("text");
  expect(request.layout).toBe("mirror");
  expect(request.strict).toBeUndefined();
  expect(request.maxEntries).toBeUndefined();
});

test("关闭面板时迟到的准备结果必须被释放，不能留一份无人认领的计划", async () => {
  const store = useExtractionStore();
  let late: ((value: ExtractionPreview) => void) | null = null;
  mocks.Prepare.mockImplementationOnce(
    () => new Promise<ExtractionPreview>((resolve) => { late = resolve; }),
  );
  void store.open(["map/forest/a.map"], "t");
  // 请求是排队发出的，先让它真的飞出去，否则测到的是"未发出"这条正确的空转分支。
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(mocks.Prepare).toHaveBeenCalledTimes(1);
  expect(store.preparing).toBe(true);
  store.close();
  late?.(preview({ id: "plan-late", exportedCount: 1 }));
  // 释放动作在服务端调用之后才发生，等一个宏任务把这条链跑完。
  await new Promise((resolve) => setTimeout(resolve, 0));
  // 计划缓存在服务端且带 TTL；不释放的话它会一直占着扫描器与闭包。
  expect(mocks.Release).toHaveBeenCalledWith("plan-late");
  expect(store.preview).toBeNull();
});

test("取消一个类别后请求带其余类别", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  store.toggleCategory("mob", false);
  await settle();
  expect(store.checked).toEqual(["scene", "ani"]);
  expect(lastRequest().categories).toEqual(["scene", "ani"]);
});

test("不允许把类别取消到空，因为空在服务端等于全部", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  for (const row of categories) store.toggleCategory(row.id, false);
  await settle();
  expect(store.checked.length).toBeGreaterThan(0);
  expect(store.hint).toContain("至少保留一个类别");
});

test("严格子集与登记建议只在打开时进入请求", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  store.strict = true;
  store.lstSuggest = true;
  await settle();
  expect(lastRequest().strict).toBe(true);
  expect(lastRequest().lstSuggest).toBe(true);
});

test("图片容器默认不拷，勾上才进请求", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(lastRequest().copyAssets).toBeUndefined();
  store.copyAssets = true;
  await settle();
  expect(lastRequest().copyAssets).toBe(true);
});

test("没有种子就不请求，并把原因写在错误里", async () => {
  const store = useExtractionStore();
  void store.open([], "t");
  await nextTick();
  expect(mocks.Prepare).not.toHaveBeenCalled();
  expect(store.error).toContain("种子");
});

test("在目录对话框里取消不应结束会话，也不该报错", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  mocks.ExecuteDialog.mockResolvedValueOnce(null);
  await store.execute();
  expect(store.visible).toBe(true);
  expect(store.preview).not.toBeNull();
  expect(store.error).toBe("");
  expect(mocks.Release).not.toHaveBeenCalled();
});

test("准备就绪后允许执行，落点由对话框决定", async () => {
  const store = useExtractionStore();
  const waiting = store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.canExport).toBe(true);
  await store.execute();
  expect(await waiting).toBe("D:/out/pvf");
  expect(store.visible).toBe(false);
  // 前端不再传路径：手输绝对路径写错只会得到一个拒绝写入的目录。
  expect(mocks.ExecuteDialog).toHaveBeenCalledWith("plan-1");
  expect(mocks.Release).toHaveBeenCalledWith("plan-1");
});

test("预览过期必须让旧预览作废并提示重取，而不是留在原地重复点击", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  mocks.ExecuteDialog.mockRejectedValueOnce(new Error("提取预览已过期，请重新准备"));
  await store.execute();
  expect(store.preview).toBeNull();
  expect(store.error).toContain("过期");
  expect(mocks.Release).toHaveBeenCalledWith("plan-1");
});

test("归档变化使预览失效", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.preview).not.toBeNull();
  mocks.events.get("archive:changed")?.();
  await nextTick();
  expect(store.preview).toBeNull();
  expect(store.error).toContain("重新准备");
});

test("手输种子支持关系名:ID，并按行去重合并", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  const lineFeed = String.fromCharCode(10);
  const typed = [" 怪物:10021", "map:1，x ", "", ""].join(lineFeed);
  expect(parseExtraSeeds(typed)).toEqual(["怪物:10021", "map:1，x"]);
  store.extraSeeds = ["怪物:10021", "map/forest/a.map"].join(lineFeed);
  store.acceptExtraSeeds();
  await settle();
  // 去重发生在合并之后：同一路径从右键与手输各来一次也只算一个种子。
  expect(lastRequest().seeds).toEqual(["map/forest/a.map", "怪物:10021"]);
});

test("连续改选项只重取一次，不逐字符走闭包", async () => {
  vi.useFakeTimers();
  try {
    const store = useExtractionStore();
    void store.open(["map/forest/a.map"], "t");
    await vi.advanceTimersByTimeAsync(0);
    const before = mocks.Prepare.mock.calls.length;
    store.maxEntries = 10;
    store.maxEntries = 100;
    store.strict = true;
    await vi.advanceTimersByTimeAsync(400);
    expect(mocks.Prepare.mock.calls.length).toBe(before + 1);
    expect(lastRequest().maxEntries).toBe(100);
    expect(lastRequest().strict).toBe(true);
  } finally {
    vi.useRealTimers();
  }
});

test("计数与覆盖率助手对缺字段安全", () => {
  expect(unresolvedCount(preview())).toBe(95);
  expect(stateTotal(preview())).toBe(995);
  expect(unresolvedCount(null)).toBe(0);
  expect(stateTotal({ ...preview(), states: null })).toBe(0);
  expect(coverageLabel(0.5512)).toBe("55.1%");
  expect(coverageLabel(Number.NaN)).toBe("—");
  expect(bytesLabel(40960)).toBe("40.0 KB");
  expect(bytesLabel(1024 * 1024 * 3)).toBe("3.00 MB");
  expect(bytesLabel(0)).toBe("0 B");
});

test("预设只改类别默认值，并按选择送进请求", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.presets.map((item) => item.id)).toEqual(["dungeon", "equipment"]);
  expect(lastRequest().preset).toBeUndefined();

  store.applyPreset("equipment");
  await settle();
  expect(store.checked).toEqual(["item", "attach", "ani"]);
  expect(lastRequest().preset).toBe("equipment");
  expect(lastRequest().categories).toEqual(["item", "attach", "ani"]);

  store.applyPreset("");
  await settle();
  expect(store.checked).toEqual([]);
  expect(lastRequest().preset).toBeUndefined();
});

test("预设取不到时面板照常工作，不把错误抛给用户", async () => {
  mocks.Presets.mockRejectedValue(new Error("服务未就绪"));
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.presets).toEqual([]);
  expect(store.error).toBe("");
  expect(store.preview).not.toBeNull();
});

test("lst 片段行原样穿过拆分与合并，不被制表符切碎", () => {
  // 片段行是"ID<TAB>路径"：拆分只按行/逗号/分号，制表符必须留着，
  // 否则落到服务端的就不是清单行而是一堆碎片。
  const lineFeed = String.fromCharCode(10);
  const fragment = "46700\tequipment/character/swordman/coat/x.equ";
  expect(parseExtraSeeds(fragment)).toEqual([fragment]);
  expect(parseExtraSeeds([fragment, "map:1"].join(lineFeed))).toEqual([fragment, "map:1"]);
});

test("「清单所属表」下拉给出表名与行数，选中后进入请求", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.relations.map((item) => item.name)).toEqual(["任务", "装备"]);
  expect(store.relationOptions.map((option) => option.label)).toEqual([
    "任务（这份归档里没有这张表）",
    "装备 · 119532 行",
  ]);
  // 不选表时不进请求：服务端据此决定纯 ID 要不要走歧义守卫。
  expect(lastRequest().listRelation).toBeUndefined();
  store.listRelation = "装备";
  await settle();
  expect(lastRequest().listRelation).toBe("装备");
});

test("表清单取不到时面板照常工作，退回纯 ID 的老规则", async () => {
  mocks.Relations.mockRejectedValue(new Error("服务未就绪"));
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(store.relations).toEqual([]);
  expect(store.relationOptions).toEqual([]);
  expect(store.error).toBe("");
  expect(store.preview).not.toBeNull();
});

test("每次打开都重新取表清单：行数是这份归档的事实，不能跨归档沿用", async () => {
  const store = useExtractionStore();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(mocks.Relations).toHaveBeenCalledTimes(1);
  store.close();
  await nextTick();
  void store.open(["map/forest/a.map"], "t");
  await settle();
  expect(mocks.Relations).toHaveBeenCalledTimes(2);
});

test("relationLabel 对缺表与零行分得清", () => {
  expect(relationLabel({ name: "装备", rows: 119532, listPath: "equipment/equipment.lst" })).toBe("装备 · 119532 行");
  expect(relationLabel({ name: "任务", rows: -1 })).toBe("任务（这份归档里没有这张表）");
  expect(relationLabel({ name: "区域", rows: 0 })).toBe("区域 · 0 行");
});
