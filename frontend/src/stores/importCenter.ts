import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { Events } from "@wailsio/runtime";
import { ImportCenterService } from "../../bindings/pvfine/services";
import type {
  ImportCenterApplyResult,
  ImportCenterEntry,
  ImportCenterPreview,
  ImportCounts,
  ImportListCounts,
  ImportListEntry,
  ImportNPKCounts,
  ImportNPKEntry,
  ImportNPKPreview,
  ImportNPKResult,
} from "../../bindings/pvfine/services/models";
import { bytesLabel } from "./extraction";

// 与 Go 侧 importCenterPageLimit 一致。一次取 200 行，表格只渲染已加载的部分。
export const importPageSize = 200;

export type ImportState = "new" | "overwrite" | "unchanged" | "unsupported" | "invalid";
export type ImportFilter = "all" | ImportState;
export type ImportTagType = "success" | "warning" | "default" | "info" | "error";

export const importStateLabels: Record<ImportState, string> = {
  new: "新增",
  overwrite: "覆盖",
  unchanged: "未变",
  unsupported: "不支持",
  invalid: "无效",
};

export const importStateTagTypes: Record<ImportState, ImportTagType> = {
  new: "success",
  overwrite: "warning",
  unchanged: "default",
  unsupported: "info",
  invalid: "error",
};

export const importFilterOptions: ReadonlyArray<{ value: ImportFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "new", label: importStateLabels.new },
  { value: "overwrite", label: importStateLabels.overwrite },
  { value: "unchanged", label: importStateLabels.unchanged },
  { value: "unsupported", label: importStateLabels.unsupported },
  { value: "invalid", label: importStateLabels.invalid },
];

const emptyCounts: ImportCounts = {
  total: 0,
  new: 0,
  overwrite: 0,
  unchanged: 0,
  unsupported: 0,
  invalid: 0,
  selected: 0,
};

const emptyListCounts: ImportListCounts = {
  total: 0,
  new: 0,
  registered: 0,
  idTaken: 0,
  duplicate: 0,
  dangling: 0,
  noTable: 0,
  selected: 0,
};

const emptyNPKCounts: ImportNPKCounts = {
  containers: 0,
  entries: 0,
  identical: 0,
  different: 0,
  missing: 0,
  unmapped: 0,
  selected: 0,
  totalBytes: 0,
  prunedBytes: 0,
};

// 只有这两种状态会被 Go 侧接受写入；其余状态即使传过去也会被忽略。
export function isWritableState(state: string): boolean {
  return state === "new" || state === "overwrite";
}

export function importStateLabel(state: string): string {
  return importStateLabels[state as ImportState] ?? state;
}

export function importStateTagType(state: string): ImportTagType {
  return importStateTagTypes[state as ImportState] ?? "default";
}

// 与提取侧同一套类型码：1 脚本、3 文本。未知类型原样显示，不伪装成文本。
export function importTypeLabel(dataType: number): string {
  if (dataType === 1) return "脚本";
  if (dataType === 3) return "文本";
  return String(dataType);
}

// 大小口径与提取中心保持一致，避免同一份产物在两处显示成不同单位。
export function importSizeLabel(size: number): string {
  return bytesLabel(size);
}

export type ImportListState =
  | "new"
  | "registered"
  | "id-taken"
  | "duplicate"
  | "dangling"
  | "no-table";

export type ImportListResolution = "skip" | "overwrite" | "remove-old";

export const importListStateLabels: Record<ImportListState, string> = {
  new: "新增",
  registered: "已登记",
  "id-taken": "ID 冲突",
  duplicate: "路径重复",
  dangling: "悬空",
  "no-table": "表缺失",
};

export const importListStateTagTypes: Record<ImportListState, ImportTagType> = {
  new: "success",
  registered: "default",
  "id-taken": "warning",
  duplicate: "warning",
  dangling: "info",
  "no-table": "error",
};

export function importListStateLabel(state: string): string {
  return importListStateLabels[state as ImportListState] ?? state;
}

export function importListStateTagType(state: string): ImportTagType {
  return importListStateTagTypes[state as ImportListState] ?? "default";
}

// 与 Go 侧一致：registered / dangling / no-table 只报告，不接受写入。
export function isSelectableListState(state: string): boolean {
  return state === "new" || state === "id-taken" || state === "duplicate";
}

/** 冲突行可选的消解方式；非冲突行返回 null，界面上显示「—」。 */
export function importListResolutionOptions(
  state: string,
): Array<{ label: string; value: ImportListResolution }> | null {
  if (state === "id-taken") {
    return [
      { label: "跳过", value: "skip" },
      { label: "改指向", value: "overwrite" },
    ];
  }
  if (state === "duplicate") {
    return [
      { label: "跳过", value: "skip" },
      { label: "删旧行", value: "remove-old" },
    ];
  }
  return null;
}

/** 勾上一行冲突项时默认采用的处理方式：只有它才真正会写表。 */
export function defaultListResolution(state: string): ImportListResolution {
  if (state === "id-taken") return "overwrite";
  if (state === "duplicate") return "remove-old";
  return "skip";
}

export type ImportNPKState = "identical" | "different" | "target-missing" | "unmapped";

export const importNPKStateLabels: Record<ImportNPKState, string> = {
  identical: "可精简",
  different: "同名不同内容，保留",
  "target-missing": "目标没有",
  unmapped: "无法比对",
};

export const importNPKStateTagTypes: Record<ImportNPKState, ImportTagType> = {
  identical: "success",
  different: "error",
  "target-missing": "default",
  unmapped: "info",
};

export function importNPKStateLabel(state: string): string {
  return importNPKStateLabels[state as ImportNPKState] ?? state;
}

export function importNPKStateTagType(state: string): ImportTagType {
  return importNPKStateTagTypes[state as ImportNPKState] ?? "default";
}

// 只有「同名同字节」能精简：同名不同字节是覆盖，不能当成重复丢掉。
export function isPrunableNPKState(state: string): boolean {
  return state === "identical";
}

function errorText(value: unknown): string {
  const text = (value as { message?: unknown } | null)?.message ?? value;
  return String(text ?? "未知错误");
}

// 计划过期/被替换时后端只说"过期"；这时候留住旧行没有意义，直接退回选择目录。
function isStaleText(text: string): boolean {
  return text.includes("过期") || text.includes("不存在");
}

export const useImportCenterStore = defineStore("importCenter", () => {
  const visible = ref(false);
  const title = ref("导入中心");
  // preview 只做摘要；行数据按页追加，200,000 条时内存里也只有看过的那部分。
  const preview = ref<ImportCenterPreview | null>(null);
  const entries = ref<ImportCenterEntry[]>([]);
  const next = ref(-1);
  const total = ref(0);
  const filter = ref<ImportFilter>("all");
  const opening = ref(false);
  const loading = ref(false);
  const selecting = ref(false);
  const running = ref(false);
  const error = ref("");
  // 归档/版本变化导致强制关闭时留给界面层弹 toast，store 里不直接调 naive-ui。
  const notice = ref("");

  // 登记表页：与文件页同样按页追加，但游标、错误与勾选队列完全独立。
  const listEntries = ref<ImportListEntry[]>([]);
  const listNext = ref(-1);
  const listTotal = ref(0);
  const listLoading = ref(false);
  const listSelecting = ref(false);
  const listStarted = ref(false);
  const listError = ref("");

  // 图片页：比对完成前没有可读的分页，所以「有没有 npkPreview」就是计划状态。
  const npkPreview = ref<ImportNPKPreview | null>(null);
  const npkEntries = ref<ImportNPKEntry[]>([]);
  const npkNext = ref(-1);
  const npkTotal = ref(0);
  const npkLoading = ref(false);
  const npkPlanning = ref(false);
  const npkSelecting = ref(false);
  const npkExecuting = ref(false);
  const npkError = ref("");

  let activeID = "";
  let generation = 0;
  let session = 0;
  let selectionStamp = 0;
  let selectionQueue: Promise<void> = Promise.resolve();
  // 已加载行上的勾选意图。翻页回来的快照可能早于这些改动，合并新页时用它兜底。
  const overrides = new Map<string, boolean>();
  // 已加载路径索引：分页合并时不必每页重建一次 Set（20 万条时那是 O(n²)）。
  const loadedPaths = new Set<string>();

  let listStamp = 0;
  let listQueue: Promise<void> = Promise.resolve();
  let listPending = 0;
  const listOverrides = new Map<string, { selected: boolean; resolution: string }>();
  const listLoadedKeys = new Set<string>();

  let npkStamp = 0;
  let npkPageToken = 0;
  let npkQueue: Promise<void> = Promise.resolve();
  let npkPending = 0;
  const npkOverrides = new Map<string, boolean>();
  const npkLoadedKeys = new Set<string>();

  const counts = computed<ImportCounts>(() => preview.value?.counts ?? emptyCounts);
  const listCounts = computed<ImportListCounts>(() => preview.value?.lists ?? emptyListCounts);
  const npkCounts = computed<ImportNPKCounts>(() => npkPreview.value?.counts ?? emptyNPKCounts);
  const hasMore = computed(() => next.value >= 0);
  const loadedCount = computed(() => entries.value.length);
  const busy = computed(
    () => opening.value || loading.value || selecting.value || running.value,
  );
  const filteredEntries = computed(() =>
    filter.value === "all"
      ? entries.value
      : entries.value.filter((entry) => entry.state === filter.value),
  );
  const selectedLoadedCount = computed(() =>
    entries.value.reduce(
      (sum, entry) => sum + (isWritableState(entry.state) && entry.selected ? 1 : 0),
      0,
    ),
  );
  // 新增条目在服务端默认勾选，所以"已选"可能大于本页看到的勾选数。差值就是尚未加载的行。
  const hiddenSelectedCount = computed(() =>
    Math.max(0, counts.value.selected - selectedLoadedCount.value),
  );
  const canSelectAll = computed(
    () => !busy.value && entries.value.some((entry) => isWritableState(entry.state) && !entry.selected),
  );
  const canClear = computed(
    () => !busy.value && entries.value.some((entry) => isWritableState(entry.state) && entry.selected),
  );
  const canExecute = computed(
    () =>
      visible.value &&
      !!preview.value &&
      !busy.value &&
      !listSelecting.value &&
      !npkExecuting.value &&
      (counts.value.selected > 0 || listCounts.value.selected > 0),
  );

  const listHasMore = computed(() => listNext.value >= 0);
  const listLoadedCount = computed(() => listEntries.value.length);
  const listRemaining = computed(() => Math.max(0, listTotal.value - listLoadedCount.value));
  const listBusy = computed(() => listLoading.value || listSelecting.value);

  const npkHasMore = computed(() => npkNext.value >= 0);
  const npkLoadedCount = computed(() => npkEntries.value.length);
  const npkRemaining = computed(() => Math.max(0, npkTotal.value - npkLoadedCount.value));
  const npkBusy = computed(() => npkLoading.value || npkPlanning.value || npkExecuting.value);
  const canExecuteNPK = computed(
    () =>
      visible.value &&
      !!npkPreview.value &&
      !npkPlanning.value &&
      !npkSelecting.value &&
      !npkExecuting.value &&
      !running.value &&
      npkCounts.value.entries > 0,
  );

  function releasePlan(id: string): void {
    if (id) void ImportCenterService.Release(id).catch(() => {});
  }

  /** 丢弃登记表页与图片页的游标与行；换产物时漏掉一处就会混进上一份产物的数据。 */
  function clearTabState(): void {
    listEntries.value = [];
    listNext.value = -1;
    listTotal.value = 0;
    listLoading.value = false;
    listSelecting.value = false;
    listStarted.value = false;
    listError.value = "";
    listOverrides.clear();
    listLoadedKeys.clear();
    listStamp++;
    listPending = 0;

    npkPreview.value = null;
    npkEntries.value = [];
    npkNext.value = -1;
    npkTotal.value = 0;
    npkLoading.value = false;
    npkPlanning.value = false;
    npkSelecting.value = false;
    npkExecuting.value = false;
    npkError.value = "";
    npkOverrides.clear();
    npkLoadedKeys.clear();
    npkStamp++;
    npkPageToken++;
    npkPending = 0;
  }

  /** 丢弃当前计划与已加载行；返回被丢弃的计划 id。调用方决定是否同时关掉模态。 */
  function clearPlanState(): string {
    const id = activeID;
    generation++;
    session++;
    activeID = "";
    preview.value = null;
    entries.value = [];
    overrides.clear();
    loadedPaths.clear();
    next.value = -1;
    total.value = 0;
    filter.value = "all";
    opening.value = false;
    loading.value = false;
    selecting.value = false;
    error.value = "";
    clearTabState();
    return id;
  }

  function resetPlan(): void {
    releasePlan(clearPlanState());
  }

  function applyPreviewCounts(current: ImportCenterPreview | null): void {
    if (!preview.value || !current) return;
    preview.value = {
      ...preview.value,
      counts: current.counts,
      lists: current.lists,
      warnings: current.warnings,
    };
  }

  function nudgeSelected(delta: number): void {
    const current = preview.value;
    if (!current || delta === 0) return;
    preview.value = {
      ...current,
      counts: {
        ...current.counts,
        selected: Math.max(0, current.counts.selected + delta),
      },
    };
  }

  function mergeEntries(rows: ImportCenterEntry[]): void {
    if (!rows.length) return;
    const appended: ImportCenterEntry[] = [];
    for (const row of rows) {
      // 分页可能因重试重叠，按归档路径去重；只有新行才追加。
      if (loadedPaths.has(row.archivePath)) continue;
      loadedPaths.add(row.archivePath);
      const intended = overrides.get(row.archivePath);
      appended.push(intended === undefined ? row : { ...row, selected: intended });
    }
    if (appended.length) entries.value = [...entries.value, ...appended];
  }

  async function loadPage(id: string, cursor: number, token: number): Promise<void> {
    loading.value = true;
    try {
      const page = await ImportCenterService.Page(id, cursor, importPageSize);
      if (!visible.value || token !== generation || activeID !== id) return;
      if (!page) {
        error.value = "后端没有返回条目分页";
        return;
      }
      mergeEntries(page.entries ?? []);
      next.value = page.next;
      // 防御：空页却声称还有下一页时停住，避免"加载更多"原地打转。
      if (next.value >= 0 && (page.entries?.length ?? 0) === 0) next.value = -1;
      total.value = page.total;
    } catch (value) {
      if (!visible.value || token !== generation || activeID !== id) return;
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else error.value = text;
    } finally {
      if (token === generation) loading.value = false;
    }
  }

  function expirePlan(text: string): void {
    releasePlan(clearPlanState());
    error.value = text;
  }

  function open(): void {
    if (visible.value) return;
    resetPlan();
    notice.value = "";
    title.value = "导入中心";
    running.value = false;
    visible.value = true;
  }

  function close(): void {
    if (running.value) return;
    resetPlan();
    visible.value = false;
  }

  function consumeNotice(): void {
    notice.value = "";
  }

  async function chooseDirectory(): Promise<void> {
    if (opening.value || running.value) return;
    const currentSession = session;
    opening.value = true;
    try {
      // 取消返回 null，不是错误：保留当前预览，什么都不做。
      const chosen = await ImportCenterService.OpenDialog();
      if (!visible.value || currentSession !== session) {
        if (chosen?.id) releasePlan(chosen.id);
        return;
      }
      if (!chosen) return;
      const previous = activeID;
      const token = ++generation;
      activeID = chosen.id;
      preview.value = chosen;
      entries.value = [];
      overrides.clear();
      loadedPaths.clear();
      next.value = -1;
      total.value = chosen.counts.total;
      filter.value = "all";
      error.value = "";
      clearTabState();
      releasePlan(previous);
      await loadPage(chosen.id, 0, token);
    } catch (value) {
      if (visible.value && currentSession === session) {
        // 原生对话框取消可能以 cancel 错误的形式回来，那不算失败。
        const text = errorText(value);
        if (!text.toLowerCase().includes("cancel")) error.value = text;
      }
    } finally {
      if (currentSession === session) opening.value = false;
    }
  }

  function loadMore(): void {
    if (busy.value || !preview.value || next.value < 0) return;
    void loadPage(preview.value.id, next.value, generation);
  }

  /** 分页读失败（非过期）后从上次的位置再试，首屏失败就从 0 开始。 */
  function retry(): void {
    if (busy.value || !preview.value) return;
    error.value = "";
    void loadPage(preview.value.id, next.value >= 0 ? next.value : 0, generation);
  }

  function rollback(before: Array<[string, boolean]>): void {
    const previous = new Map(before);
    let delta = 0;
    entries.value = entries.value.map((entry) => {
      const was = previous.get(entry.archivePath);
      if (was === undefined) return entry;
      if (entry.selected !== was) delta += was ? 1 : -1;
      return { ...entry, selected: was };
    });
    for (const [path, was] of before) overrides.set(path, was);
    nudgeSelected(delta);
  }

  async function sendSelection(
    id: string,
    paths: string[],
    on: boolean,
    before: Array<[string, boolean]>,
    stamp: number,
  ): Promise<void> {
    if (!visible.value || activeID !== id) return;
    try {
      const updated = await ImportCenterService.SetSelected(id, paths, on);
      if (!visible.value || activeID !== id) return;
      // 串行发送时只有最后一笔的结果代表最新状态，中间结果不回写以免计数闪回。
      if (updated && stamp === selectionStamp) applyPreviewCounts(updated);
    } catch (value) {
      if (!visible.value || activeID !== id) return;
      rollback(before);
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else error.value = text;
    }
  }

  /** 乐观更新已加载行，再把请求排进串行队列；返回的 Promise 供批量操作显示 busy。 */
  function applySelection(paths: string[], on: boolean): Promise<void> {
    const current = preview.value;
    if (!visible.value || !current || !paths.length) return Promise.resolve();
    const targets = new Set(paths);
    const before: Array<[string, boolean]> = [];
    for (const entry of entries.value) {
      if (targets.has(entry.archivePath) && isWritableState(entry.state) && entry.selected !== on) {
        before.push([entry.archivePath, entry.selected]);
      }
    }
    if (!before.length) return Promise.resolve();
    const changed = new Set(before.map(([path]) => path));
    for (const path of changed) overrides.set(path, on);
    entries.value = entries.value.map((entry) =>
      changed.has(entry.archivePath) ? { ...entry, selected: on } : entry,
    );
    nudgeSelected(on ? before.length : -before.length);

    const id = current.id;
    const stamp = ++selectionStamp;
    const action = selectionQueue
      .catch(() => {})
      .then(() => sendSelection(id, paths, on, before, stamp));
    selectionQueue = action;
    return action;
  }

  function setRowSelected(archivePath: string, on: boolean): void {
    if (busy.value) return;
    const row = entries.value.find((entry) => entry.archivePath === archivePath);
    if (!row || !isWritableState(row.state) || row.selected === on) return;
    void applySelection([archivePath], on);
  }

  /**
   * 全选/全不选只提交"当前已加载且状态可写"的路径。
   * 未加载的行不动：一份 20 万条的产物不可能一次把路径全传过去。
   */
  async function selectLoaded(on: boolean): Promise<void> {
    if (busy.value) return;
    const paths = entries.value
      .filter((entry) => isWritableState(entry.state) && entry.selected !== on)
      .map((entry) => entry.archivePath);
    if (!paths.length) return;
    selecting.value = true;
    try {
      await applySelection(paths, on);
    } finally {
      selecting.value = false;
    }
  }

  // ---------------------------------------------------------------- 登记表页

  function mergeListEntries(rows: ImportListEntry[]): void {
    if (!rows.length) return;
    const appended: ImportListEntry[] = [];
    for (const row of rows) {
      if (listLoadedKeys.has(row.key)) continue;
      listLoadedKeys.add(row.key);
      const intended = listOverrides.get(row.key);
      appended.push(
        intended ? { ...row, selected: intended.selected, resolution: intended.resolution } : row,
      );
    }
    if (appended.length) listEntries.value = [...listEntries.value, ...appended];
  }

  async function loadListPage(id: string, cursor: number, token: number): Promise<void> {
    listLoading.value = true;
    try {
      const page = await ImportCenterService.ListPage(id, cursor, importPageSize);
      if (!visible.value || token !== generation || activeID !== id) return;
      if (!page) {
        listError.value = "后端没有返回登记表分页";
        return;
      }
      mergeListEntries(page.entries ?? []);
      listNext.value = page.next;
      if (listNext.value >= 0 && (page.entries?.length ?? 0) === 0) listNext.value = -1;
      listTotal.value = page.total;
    } catch (value) {
      if (!visible.value || token !== generation || activeID !== id) return;
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else listError.value = text;
    } finally {
      if (token === generation) listLoading.value = false;
    }
  }

  /** 切到登记表页时才发第一次请求；没有片段就不必问后端。 */
  function ensureList(): void {
    const current = preview.value;
    if (!visible.value || !current || listStarted.value || listLoading.value) return;
    listStarted.value = true;
    listTotal.value = current.lists.total;
    if (current.lists.total === 0) return;
    void loadListPage(current.id, 0, generation);
  }

  function loadListMore(): void {
    if (!visible.value || !preview.value || listBusy.value || running.value) return;
    if (listNext.value < 0) return;
    void loadListPage(preview.value.id, listNext.value, generation);
  }

  function retryList(): void {
    if (!visible.value || !preview.value || listLoading.value) return;
    listError.value = "";
    listStarted.value = true;
    void loadListPage(
      preview.value.id,
      listNext.value >= 0 ? listNext.value : 0,
      generation,
    );
  }

  function nudgeListSelected(delta: number): void {
    const current = preview.value;
    if (!current || delta === 0) return;
    preview.value = {
      ...current,
      lists: {
        ...current.lists,
        selected: Math.max(0, current.lists.selected + delta),
      },
    };
  }

  function rollbackList(before: Map<string, { selected: boolean; resolution: string }>): void {
    let delta = 0;
    listEntries.value = listEntries.value.map((entry) => {
      const was = before.get(entry.key);
      if (!was) return entry;
      if (entry.selected !== was.selected) delta += was.selected ? 1 : -1;
      listOverrides.set(entry.key, was);
      return { ...entry, selected: was.selected, resolution: was.resolution };
    });
    nudgeListSelected(delta);
  }

  async function sendListDecision(
    id: string,
    key: string,
    selected: boolean,
    resolution: string,
    before: Map<string, { selected: boolean; resolution: string }>,
    stamp: number,
  ): Promise<void> {
    if (!visible.value || activeID !== id) return;
    try {
      const updated = await ImportCenterService.SetListDecision(id, key, selected, resolution);
      if (!visible.value || activeID !== id) return;
      // 队列里只有最后一笔代表最新意图，中间结果不回写，免得摘要闪回。
      if (updated && stamp === listStamp) applyPreviewCounts(updated);
    } catch (value) {
      if (!visible.value || activeID !== id) return;
      rollbackList(before);
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else listError.value = text;
    }
  }

  /** 勾选与消解方式共用一个入口：两者都会写表，必须串行。 */
  function setListDecision(key: string, selected: boolean, resolution: string): void {
    const current = preview.value;
    if (!visible.value || !current || running.value) return;
    const row = listEntries.value.find((entry) => entry.key === key);
    if (!row) return;
    if (row.selected === selected && row.resolution === resolution) return;
    if (selected && !isSelectableListState(row.state)) return;
    const before = new Map([[key, { selected: row.selected, resolution: row.resolution }]]);
    listOverrides.set(key, { selected, resolution });
    listEntries.value = listEntries.value.map((entry) =>
      entry.key === key ? { ...entry, selected, resolution } : entry,
    );
    if (selected !== row.selected) nudgeListSelected(selected ? 1 : -1);

    const id = current.id;
    const stamp = ++listStamp;
    listPending++;
    listSelecting.value = true;
    listQueue = listQueue
      .catch(() => {})
      .then(() => sendListDecision(id, key, selected, resolution, before, stamp))
      .finally(() => {
        listPending = Math.max(0, listPending - 1);
        listSelecting.value = listPending > 0;
      });
  }

  /**
   * 冲突行勾选时顺带选好默认消解方式：只勾选而消解方式仍是「跳过」时 Go 侧
   * 不会写这一行，勾了等于没勾，不能让界面允许这种空操作。
   */
  function toggleListRow(key: string, on: boolean): void {
    const row = listEntries.value.find((entry) => entry.key === key);
    if (!row) return;
    if (on && (row.resolution === "" || row.resolution === "skip")) {
      setListDecision(key, true, defaultListResolution(row.state));
      return;
    }
    setListDecision(key, on, row.resolution || "skip");
  }

  /** 选非「跳过」的消解方式即表示要处理这一行；选「跳过」同时取消勾选。 */
  function setListResolution(key: string, resolution: ImportListResolution): void {
    const row = listEntries.value.find((entry) => entry.key === key);
    if (!row) return;
    if (row.resolution === resolution && row.selected === (resolution !== "skip")) return;
    setListDecision(key, resolution !== "skip", resolution);
  }

  // ---------------------------------------------------------------- 图片页

  function mergeNPKEntries(rows: ImportNPKEntry[]): void {
    if (!rows.length) return;
    const appended: ImportNPKEntry[] = [];
    for (const row of rows) {
      if (npkLoadedKeys.has(row.key)) continue;
      npkLoadedKeys.add(row.key);
      const intended = npkOverrides.get(row.key);
      appended.push(intended === undefined ? row : { ...row, selected: intended });
    }
    if (appended.length) npkEntries.value = [...npkEntries.value, ...appended];
  }

  async function loadNPKPage(id: string, cursor: number, token: number): Promise<void> {
    const pageToken = ++npkPageToken;
    npkLoading.value = true;
    try {
      const page = await ImportCenterService.NPKPage(id, cursor, importPageSize);
      if (
        !visible.value ||
        token !== generation ||
        activeID !== id ||
        pageToken !== npkPageToken
      ) {
        return;
      }
      if (!page) {
        npkError.value = "后端没有返回图片分页";
        return;
      }
      mergeNPKEntries(page.entries ?? []);
      npkNext.value = page.next;
      if (npkNext.value >= 0 && (page.entries?.length ?? 0) === 0) npkNext.value = -1;
      npkTotal.value = page.total;
    } catch (value) {
      if (
        !visible.value ||
        token !== generation ||
        activeID !== id ||
        pageToken !== npkPageToken
      ) {
        return;
      }
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else npkError.value = text;
    } finally {
      if (pageToken === npkPageToken) npkLoading.value = false;
    }
  }

  /** 比对是显式动作：扫 3000 个容器不该在打开产物时顺手发生。 */
  async function planNPK(): Promise<void> {
    const current = preview.value;
    if (!visible.value || !current || npkPlanning.value || running.value) return;
    const id = current.id;
    npkPlanning.value = true;
    npkError.value = "";
    try {
      // 上一次比对留下的勾选请求必须先落完：重新比对会换掉 npkEntries，
      // 迟到的 SetNPKSelected 会改到新计划上。
      await npkQueue.catch(() => {});
      if (!visible.value || activeID !== id) return;
      const plan = await ImportCenterService.PlanNPK(id);
      if (!visible.value || activeID !== id) return;
      if (!plan) {
        npkError.value = "后端没有返回比对结果";
        return;
      }
      npkPreview.value = plan;
      npkEntries.value = [];
      npkNext.value = -1;
      npkTotal.value = plan.counts.entries;
      npkStamp++;
      npkOverrides.clear();
      npkLoadedKeys.clear();
      await loadNPKPage(id, 0, generation);
    } catch (value) {
      if (!visible.value || activeID !== id) return;
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else npkError.value = text;
    } finally {
      if (visible.value && activeID === id) npkPlanning.value = false;
    }
  }

  function loadNPKMore(): void {
    if (!visible.value || !preview.value || !npkPreview.value) return;
    if (npkBusy.value || running.value || npkNext.value < 0) return;
    void loadNPKPage(preview.value.id, npkNext.value, generation);
  }

  function nudgeNPKSelected(size: number, on: boolean): void {
    const current = npkPreview.value;
    if (!current) return;
    npkPreview.value = {
      ...current,
      counts: {
        ...current.counts,
        selected: Math.max(0, current.counts.selected + (on ? 1 : -1)),
        prunedBytes: Math.max(0, current.counts.prunedBytes + (on ? size : -size)),
      },
    };
  }

  async function sendNPKSelection(
    id: string,
    key: string,
    on: boolean,
    size: number,
    before: boolean,
    stamp: number,
  ): Promise<void> {
    if (!visible.value || activeID !== id) return;
    try {
      const updated = await ImportCenterService.SetNPKSelected(id, [key], on);
      if (!visible.value || activeID !== id) return;
      if (updated && stamp === npkStamp) npkPreview.value = updated;
    } catch (value) {
      if (!visible.value || activeID !== id) return;
      npkOverrides.set(key, before);
      npkEntries.value = npkEntries.value.map((entry) =>
        entry.key === key ? { ...entry, selected: before } : entry,
      );
      nudgeNPKSelected(size, before);
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else npkError.value = text;
    }
  }

  function setNPKSelected(key: string, on: boolean): void {
    const current = preview.value;
    if (!visible.value || !current || !npkPreview.value || running.value) return;
    if (npkPlanning.value || npkExecuting.value) return;
    const row = npkEntries.value.find((entry) => entry.key === key);
    if (!row || !isPrunableNPKState(row.state) || row.selected === on) return;
    const before = row.selected;
    npkOverrides.set(key, on);
    npkEntries.value = npkEntries.value.map((entry) =>
      entry.key === key ? { ...entry, selected: on } : entry,
    );
    nudgeNPKSelected(row.size, on);

    const id = current.id;
    const stamp = ++npkStamp;
    npkPending++;
    npkSelecting.value = true;
    npkQueue = npkQueue
      .catch(() => {})
      .then(() => sendNPKSelection(id, key, on, row.size, before, stamp))
      .finally(() => {
        npkPending = Math.max(0, npkPending - 1);
        npkSelecting.value = npkPending > 0;
      });
  }

  /** 选目录 + 写出。取消目录对话框返回 null，调用方不要当失败处理。 */
  async function executeNPK(): Promise<ImportNPKResult | null> {
    if (!canExecuteNPK.value || !preview.value) return null;
    const id = preview.value.id;
    npkError.value = "";
    npkExecuting.value = true;
    try {
      const outputDir = await ImportCenterService.PickDirectory("选择精简后的 NPK 输出目录");
      if (!visible.value || activeID !== id) return null;
      if (!outputDir) return null;
      // 逐条勾选是串行的，先把队列落完，否则写出的容器可能是勾选前的样子。
      await npkQueue.catch(() => {});
      if (!visible.value || activeID !== id) return null;
      const result = await ImportCenterService.ExecuteNPK(id, outputDir);
      if (!result) {
        npkError.value = "后端没有返回精简结果";
        return null;
      }
      return result;
    } catch (value) {
      if (visible.value && activeID === id) {
        const text = errorText(value);
        if (isStaleText(text)) expirePlan(text);
        else npkError.value = text;
      }
      return null;
    } finally {
      npkExecuting.value = false;
    }
  }

  // ---------------------------------------------------------------- 执行

  async function execute(): Promise<ImportCenterApplyResult | null> {
    if (!canExecute.value || !preview.value) return null;
    const id = preview.value.id;
    running.value = true;
    error.value = "";
    try {
      // 逐行勾选是异步串行的：先把还在队列里的 SetSelected / 判定落完，
      // 否则一次很快的 Execute 可能读到勾选前的快照。
      await Promise.all([
        selectionQueue.catch(() => {}),
        listQueue.catch(() => {}),
        npkQueue.catch(() => {}),
      ]);
      if (!visible.value || activeID !== id) return null;
      const result = await ImportCenterService.Execute(id);
      if (!result) {
        error.value = "后端没有返回导入结果";
        return null;
      }
      // 后端成功后会自己清掉计划，Release 只是兜底；失败时计划仍保留，方便重试。
      resetPlan();
      visible.value = false;
      return result;
    } catch (value) {
      const text = errorText(value);
      if (isStaleText(text)) expirePlan(text);
      else error.value = text;
      return null;
    } finally {
      running.value = false;
    }
  }

  function invalidate(): void {
    // 执行中要保住这次写入的结果提示；原生目录对话框打开时也不该被后台事件打断。
    if (!visible.value || running.value || opening.value) return;
    resetPlan();
    visible.value = false;
    notice.value = "归档或版本已变化，请重新选择产物";
  }

  Events.On("archive:changed", invalidate);
  Events.On("archive:opened", invalidate);
  Events.On("archive:closed", invalidate);
  Events.On("version:changed", invalidate);
  Events.On("version:committed", invalidate);

  return {
    visible,
    title,
    preview,
    entries,
    counts,
    total,
    filter,
    opening,
    loading,
    selecting,
    running,
    error,
    notice,
    hasMore,
    loadedCount,
    busy,
    filteredEntries,
    hiddenSelectedCount,
    canSelectAll,
    canClear,
    canExecute,
    open,
    close,
    chooseDirectory,
    loadMore,
    retry,
    setRowSelected,
    selectLoaded,
    execute,
    invalidate,
    consumeNotice,

    listEntries,
    listCounts,
    listTotal,
    listLoading,
    listSelecting,
    listStarted,
    listError,
    listHasMore,
    listLoadedCount,
    listRemaining,
    listBusy,
    ensureList,
    loadListMore,
    retryList,
    setListDecision,
    toggleListRow,
    setListResolution,

    npkPreview,
    npkEntries,
    npkCounts,
    npkTotal,
    npkLoading,
    npkPlanning,
    npkSelecting,
    npkExecuting,
    npkError,
    npkHasMore,
    npkLoadedCount,
    npkRemaining,
    npkBusy,
    canExecuteNPK,
    planNPK,
    loadNPKMore,
    setNPKSelected,
    executeNPK,
  };
});
