import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createPinia, disposePinia, setActivePinia, type Pinia } from "pinia";

const mocks = vi.hoisted(() => ({
  OpenDialog: vi.fn(),
  Page: vi.fn(),
  SetSelected: vi.fn(),
  ListPage: vi.fn(),
  SetListDecision: vi.fn(),
  PlanNPK: vi.fn(),
  NPKPage: vi.fn(),
  SetNPKSelected: vi.fn(),
  Execute: vi.fn(),
  ExecuteNPK: vi.fn(),
  PickDirectory: vi.fn(),
  Release: vi.fn(),
  events: new Map<string, () => void>(),
}));
vi.mock("../bindings/pvfine/services", () => ({ ImportCenterService: mocks }));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: (name: string, handler: () => void) => mocks.events.set(name, handler) },
}));

import {
  defaultListResolution,
  importListResolutionOptions,
  importTypeLabel,
  isPrunableNPKState,
  isSelectableListState,
  isWritableState,
  useImportCenterStore,
} from "../src/stores/importCenter";
import type {
  ImportCenterEntry,
  ImportCenterPage,
  ImportCenterPreview,
  ImportCounts,
  ImportListCounts,
  ImportListEntry,
  ImportNPKCounts,
  ImportNPKEntry,
  ImportNPKPreview,
} from "../bindings/pvfine/services/models";

function counts(overrides: Partial<ImportCounts> = {}): ImportCounts {
  return {
    total: 2, new: 1, overwrite: 0, unchanged: 1, unsupported: 0, invalid: 0, selected: 0,
    ...overrides,
  };
}
function listCounts(overrides: Partial<ImportListCounts> = {}): ImportListCounts {
  return {
    total: 0, new: 0, registered: 0, idTaken: 0, duplicate: 0, dangling: 0, noTable: 0,
    selected: 0, ...overrides,
  };
}
function npkCounts(overrides: Partial<ImportNPKCounts> = {}): ImportNPKCounts {
  return {
    containers: 0, entries: 0, identical: 0, different: 0, missing: 0, unmapped: 0,
    selected: 0, totalBytes: 0, prunedBytes: 0, ...overrides,
  };
}
function preview(overrides: Partial<ImportCenterPreview> = {}): ImportCenterPreview {
  return {
    id: "plan-1", root: "E:/export", counts: counts(), lists: listCounts(), warnings: [],
    ...overrides,
  };
}
function entry(overrides: Partial<ImportCenterEntry> = {}): ImportCenterEntry {
  return {
    archivePath: "a.txt", form: "text", dataType: 3, size: 10, category: "script",
    autoCompleted: false, state: "new", selected: false, reason: "", ...overrides,
  };
}
function page(entries: ImportCenterEntry[], next = -1, total = entries.length): ImportCenterPage {
  return { entries, cursor: 0, next, total };
}
function listEntry(overrides: Partial<ImportListEntry> = {}): ImportListEntry {
  return {
    key: "equipment.lst:100", listPath: "equipment/equipment.lst", id: "100",
    targetPath: "equip/a.equ", state: "new", resolution: "skip", selected: false,
    conflictOf: "", reason: "", ...overrides,
  };
}
function npkEntry(overrides: Partial<ImportNPKEntry> = {}): ImportNPKEntry {
  return {
    key: "sprite.NPK:0", container: "sprite.NPK", name: "a.img", size: 1024,
    targetNpk: "sprite.NPK", state: "identical", selected: false, ...overrides,
  };
}
function npkPreview(overrides: Partial<ImportNPKPreview> = {}): ImportNPKPreview {
  return {
    planId: "plan-1", targetDir: "E:/client/ImagePacks2",
    counts: npkCounts({ containers: 1, entries: 1, identical: 1, totalBytes: 1024 }),
    ...overrides,
  };
}

let pinia: Pinia;
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
  vi.clearAllMocks();
  mocks.events.clear();
  mocks.Release.mockResolvedValue(undefined);
});
afterEach(() => disposePinia(pinia));

async function openWith(plan: ImportCenterPreview, rows: ImportCenterEntry[], next = -1) {
  const store = useImportCenterStore();
  mocks.OpenDialog.mockResolvedValue(plan);
  mocks.Page.mockResolvedValue(page(rows, next));
  store.open();
  await store.chooseDirectory();
  return store;
}

test("写入门禁与消解默认值只认白名单状态", () => {
  // 只有 new/overwrite 会被 Go 侧接受；其余传过去也会被忽略。
  expect(isWritableState("new")).toBe(true);
  expect(isWritableState("overwrite")).toBe(true);
  for (const state of ["unchanged", "unsupported", "invalid"]) {
    expect(isWritableState(state)).toBe(false);
  }
  // registered/dangling/no-table 只报告，不接受写入。
  expect(isSelectableListState("new")).toBe(true);
  expect(isSelectableListState("id-taken")).toBe(true);
  expect(isSelectableListState("duplicate")).toBe(true);
  for (const state of ["registered", "dangling", "no-table"]) {
    expect(isSelectableListState(state)).toBe(false);
  }
  // 只有同名同字节能精简，同名不同字节是覆盖不是重复。
  expect(isPrunableNPKState("identical")).toBe(true);
  for (const state of ["different", "target-missing", "unmapped"]) {
    expect(isPrunableNPKState(state)).toBe(false);
  }
  expect(defaultListResolution("id-taken")).toBe("overwrite");
  expect(defaultListResolution("duplicate")).toBe("remove-old");
  expect(defaultListResolution("new")).toBe("skip");
  expect(importListResolutionOptions("registered")).toBeNull();
  expect(importTypeLabel(1)).toBe("脚本");
  expect(importTypeLabel(3)).toBe("文本");
  expect(importTypeLabel(9)).toBe("9");
});

test("选目录后按页装载、按状态过滤，并算出未加载的已选项", async () => {
  const store = await openWith(
    preview({ counts: counts({ total: 2, selected: 3 }) }),
    [entry({ archivePath: "a.txt", selected: true }), entry({ archivePath: "b.txt", state: "unchanged", selected: false })],
  );

  expect(store.visible).toBe(true);
  expect(store.entries).toHaveLength(2);
  expect(store.canExecute).toBe(true);
  // 服务端默认勾选新增行，"已选 3" 里有 2 条还没加载进来。
  expect(store.hiddenSelectedCount).toBe(2);

  store.filter = "unchanged";
  expect(store.filteredEntries.map((row) => row.archivePath)).toEqual(["b.txt"]);
});

test("分页重叠时按归档路径去重，不会把同一行塞两遍", async () => {
  const store = await openWith(preview(), [entry({ archivePath: "a.txt" })], 5);
  expect(store.hasMore).toBe(true);

  mocks.Page.mockResolvedValue(page([entry({ archivePath: "a.txt" }), entry({ archivePath: "c.txt" })], -1, 3));
  store.loadMore();
  await vi.waitFor(() => expect(store.hasMore).toBe(false));

  expect(store.entries.map((row) => row.archivePath)).toEqual(["a.txt", "c.txt"]);
});

test("全选只提交已加载且可写的行，未加载的不动", async () => {
  const store = await openWith(
    preview({ counts: counts({ total: 3 }) }),
    [
      entry({ archivePath: "new.txt", state: "new" }),
      entry({ archivePath: "over.txt", state: "overwrite" }),
      entry({ archivePath: "same.txt", state: "unchanged", selected: false }),
    ],
    9,
  );
  mocks.SetSelected.mockResolvedValue(preview({ counts: counts({ selected: 2 }) }));

  await store.selectLoaded(true);

  expect(mocks.SetSelected).toHaveBeenCalledWith("plan-1", ["new.txt", "over.txt"], true);
  // 服务端摘要作为权威值回写，已加载行的勾选状态也与它一致；不可写的行没被碰。
  expect(store.counts.selected).toBe(2);
  expect(store.entries.filter((row) => row.selected).map((row) => row.archivePath))
    .toEqual(["new.txt", "over.txt"]);
});

test("勾选失败时回滚行状态与计数，并给出错误", async () => {
  const store = await openWith(preview({ counts: counts({ selected: 1 }) }), [
    entry({ archivePath: "a.txt", selected: true }),
  ]);
  mocks.SetSelected.mockRejectedValue(new Error("写入被拒绝"));

  await store.selectLoaded(false);

  expect(store.entries[0].selected).toBe(true);
  expect(store.counts.selected).toBe(1);
  expect(store.error).toBe("写入被拒绝");
});

test("冲突行勾选时连带选好消解方式，避免勾了却写不进表", async () => {
  const store = await openWith(
    preview({ lists: listCounts({ total: 2, idTaken: 1, duplicate: 1 }) }),
    [],
  );
  mocks.ListPage.mockResolvedValue({
    entries: [
      listEntry({ key: "k1", state: "id-taken", resolution: "skip" }),
      listEntry({ key: "k2", state: "duplicate", resolution: "skip" }),
    ],
    cursor: 0, next: -1, total: 2,
  });
  mocks.SetListDecision.mockResolvedValue(preview());
  store.ensureList();
  await vi.waitFor(() => expect(store.listEntries).toHaveLength(2));

  store.toggleListRow("k1", true);
  await vi.waitFor(() => expect(mocks.SetListDecision).toHaveBeenCalledTimes(1));
  expect(mocks.SetListDecision).toHaveBeenLastCalledWith("plan-1", "k1", true, "overwrite");

  store.toggleListRow("k2", true);
  await vi.waitFor(() => expect(mocks.SetListDecision).toHaveBeenCalledTimes(2));
  expect(mocks.SetListDecision).toHaveBeenLastCalledWith("plan-1", "k2", true, "remove-old");
});

test("只报告类登记行不接受勾选，选「跳过」会同时取消勾选", async () => {
  const store = await openWith(
    preview({ lists: listCounts({ total: 2, registered: 1, idTaken: 1 }) }),
    [],
  );
  mocks.ListPage.mockResolvedValue({
    entries: [
      listEntry({ key: "done", state: "registered" }),
      listEntry({ key: "taken", state: "id-taken", resolution: "overwrite", selected: true }),
    ],
    cursor: 0, next: -1, total: 2,
  });
  mocks.SetListDecision.mockResolvedValue(preview());
  store.ensureList();
  await vi.waitFor(() => expect(store.listEntries).toHaveLength(2));

  store.setListDecision("done", true, "overwrite");
  expect(mocks.SetListDecision).not.toHaveBeenCalled();

  store.setListResolution("taken", "skip");
  await vi.waitFor(() => expect(mocks.SetListDecision).toHaveBeenCalledTimes(1));
  expect(mocks.SetListDecision).toHaveBeenLastCalledWith("plan-1", "taken", false, "skip");
});

test("NPK 只有同名同字节可勾，勾选联动精简字节数", async () => {
  const store = await openWith(preview(), []);
  mocks.PlanNPK.mockResolvedValue(npkPreview());
  mocks.NPKPage.mockResolvedValue({
    entries: [
      npkEntry({ key: "same", size: 1024 }),
      npkEntry({ key: "diff", state: "different", size: 2048 }),
    ],
    cursor: 0, next: -1, total: 2,
  });
  mocks.SetNPKSelected.mockResolvedValue(npkPreview({
    counts: npkCounts({
      containers: 1, entries: 1, identical: 1, selected: 1, totalBytes: 1024, prunedBytes: 1024,
    }),
  }));
  await store.planNPK();
  await vi.waitFor(() => expect(store.npkEntries).toHaveLength(2));

  store.setNPKSelected("diff", true);
  expect(mocks.SetNPKSelected).not.toHaveBeenCalled();

  store.setNPKSelected("same", true);
  await vi.waitFor(() => expect(mocks.SetNPKSelected).toHaveBeenCalledTimes(1));
  expect(store.npkCounts.selected).toBe(1);
  expect(store.npkCounts.prunedBytes).toBe(1024);
});

test("执行前会等排队中的勾选落完，避免写出勾选前的快照", async () => {
  const store = await openWith(
    preview({ counts: counts({ selected: 0 }) }),
    [entry({ archivePath: "a.txt", selected: false })],
  );
  let finishSelection: (value: ImportCenterPreview) => void = () => {};
  mocks.SetSelected.mockReturnValue(new Promise<ImportCenterPreview>((resolve) => {
    finishSelection = resolve;
  }));
  mocks.Execute.mockResolvedValue({ written: 1, overwritten: 0, skipped: 0 });

  store.setRowSelected("a.txt", true);
  expect(store.counts.selected).toBe(1);

  const running = store.execute();
  expect(mocks.Execute).not.toHaveBeenCalled();

  finishSelection(preview({ counts: counts({ selected: 1 }) }));
  await running;
  expect(mocks.Execute).toHaveBeenCalledWith("plan-1");
  expect(store.visible).toBe(false);
});

test("计划过期时清空预览并退回选目录，归档变化会直接关窗", async () => {
  const store = useImportCenterStore();
  mocks.OpenDialog.mockResolvedValue(preview());
  mocks.Page.mockRejectedValue(new Error("导入计划已过期，请重新选择产物"));
  store.open();
  await store.chooseDirectory();

  expect(store.preview).toBeNull();
  expect(store.entries).toEqual([]);
  expect(store.error).toContain("过期");
  expect(store.visible).toBe(true);

  mocks.OpenDialog.mockResolvedValue(preview({ id: "plan-2" }));
  mocks.Page.mockResolvedValue(page([entry({ archivePath: "a.txt" })]));
  await store.chooseDirectory();
  mocks.events.get("archive:closed")?.();
  expect(store.visible).toBe(false);
  expect(store.notice).toContain("归档或版本已变化");
  expect(store.preview).toBeNull();
});
