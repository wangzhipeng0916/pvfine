import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import { Events } from "@wailsio/runtime";
import { ExtractionService } from "../../bindings/pvfine/services";
import type {
  ExtractionCatalogStatus,
  ExtractionPreview,
  ExtractionPreset,
  ExtractionRelation,
  ExtractionRequest,
} from "../../bindings/pvfine/services/models";

export const extractionLayoutOptions = [
  { label: "原路径镜像", value: "mirror" },
  { label: "按类别分目录", value: "category" },
  { label: "按种子分目录", value: "seed" },
];

// 文本形态与「源码导入」的文本模式一一对应，所以它必须是默认值：
// 提取出来的树要能改完再导回去，而不是一份只能看的转储。
export const extractionFormOptions = [
  { label: "反编译文本（可用「源码导入」读回）", value: "text" },
  { label: "原字节", value: "raw" },
];

export const extractionStateLabels: Record<string, string> = {
  resolved: "已解析",
  missing: "目标缺失",
  unknown: "判不定",
  unmodeled: "未声明边",
};

export function bytesLabel(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(2)} MB`;
}

export function coverageLabel(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "—";
  return `${(value * 100).toFixed(1)}%`;
}

/** 没解析出来的三档合起来才是"这份产物有多少东西是不确定的"。 */
export function unresolvedCount(preview: ExtractionPreview | null): number {
  const states = preview?.states ?? {};
  return (states["missing"] ?? 0) + (states["unknown"] ?? 0) + (states["unmodeled"] ?? 0);
}

export function stateTotal(preview: ExtractionPreview | null): number {
  return Object.values(preview?.states ?? {}).reduce(
    (sum: number, value: number | undefined) => sum + (value ?? 0),
    0,
  );
}

/** 手输种子按行/逗号/分号拆开；每段可以是条目路径、`关系名:ID`、纯 ID，或 lst 片段行。 */
export function parseExtraSeeds(text: string): string[] {
  return text
    .split(/[\n\r,;]+/)
    .map((line) => line.trim())
    .filter(Boolean);
}

/** 下拉里一行说明：表名加这份归档的实际行数，选之前就知道规模。 */
export function relationLabel(relation: ExtractionRelation): string {
  const rows = relation.rows ?? -1;
  if (rows < 0) return `${relation.name}（这份归档里没有这张表）`;
  return `${relation.name} · ${rows} 行`;
}

export const useExtractionStore = defineStore("extraction", () => {
  const visible = ref(false);
  const title = ref("");
  const seeds = ref<string[]>([]);
  // 手输的种子：MOD 作者手上常常是表行号而不是文件路径，E4 的 ID 档要有入口。
  const extraSeeds = ref("");
  // 空数组表示"全部类别"，与 Go 侧 Selection.Categories 的约定一致。
  const checked = ref<string[]>([]);
  const layout = ref("mirror");
  const form = ref("text");
  const strict = ref(false);
  const lstSuggest = ref(false);
  // 图片容器（NPK）体积大，默认只出对照表；勾选后连容器一起拷进包。
  const copyAssets = ref(false);
  // 预设只给类别默认值与种子写法，不改变闭包走法。
  const presets = ref<ExtractionPreset[]>([]);
  const preset = ref("");
  const activePreset = computed(() => presets.value.find((item) => item.id === preset.value) ?? null);
  // 「清单所属表」：贴进来的纯 ID 列表属于哪张登记表。选中后纯数字行按它解析，
  // 不选时纯 ID 仍只认唯一归属（86 实测抽样里 84.7% 的 ID 被多张表同时认领）。
  const relations = ref<ExtractionRelation[]>([]);
  const listRelation = ref("");
  const relationOptions = computed(() =>
    relations.value.map((item) => ({ label: relationLabel(item), value: item.name })),
  );
  const maxEntries = ref(0);
  // 生效的边目录来源：外部文件可能悄悄覆盖内置声明，面板必须说清楚用的是哪一份。
  const catalogStatus = ref<ExtractionCatalogStatus | null>(null);
  const catalogSourceLabel = computed(() => {
    const status = catalogStatus.value;
    if (!status) return "";
    if (status.source !== "external") return `边目录：内置 ${status.edgeCount} 条`;
    if (!status.externalEdgeCount && !status.externalCategoryCount) {
      return `边目录：外部文件已就绪（未声明覆盖），生效 ${status.edgeCount} 条 · ${status.path}`;
    }
    return `边目录：外部文件声明 ${status.externalEdgeCount} 条，生效 ${status.edgeCount} 条 · ${status.path}`;
  });
  const preview = ref<ExtractionPreview | null>(null);
  const preparing = ref(false);
  const running = ref(false);
  const error = ref("");
  // 拒绝类操作要说的话，不是失败：refresh() 会清 error，用它会一闪就没。
  const hint = ref("");
  let generation = 0;
  let queue: Promise<void> = Promise.resolve();
  let complete: ((dir: string) => void) | null = null;
  let debounce: ReturnType<typeof setTimeout> | null = null;
  // 面板正在显示"还没有种子"这句引导；只有它自己该被下一次成功准备撤掉。
  let guiding = false;

  const allSeeds = computed(() => [
    ...new Set([...seeds.value, ...parseExtraSeeds(extraSeeds.value)]),
  ]);
  const selectedCount = computed(() =>
    (preview.value?.categories ?? []).filter((row) => checked.value.includes(row.id)).length,
  );
  // 目录改由原生对话框选择，所以这里不再要求用户手输绝对路径：
  // 写错一个盘符就会得到一个拒绝写入的目录，比不导出更糟。
  const canExport = computed(() =>
    visible.value && !!preview.value && !preparing.value && !running.value,
  );

  function releasePreview(): void {
    const id = preview.value?.id;
    preview.value = null;
    if (id) void ExtractionService.Release(id).catch(() => {});
  }

  function buildRequest(): ExtractionRequest {
    const request: ExtractionRequest = {
      seeds: [...allSeeds.value],
      layout: layout.value,
      form: form.value,
    };
    if (checked.value.length) request.categories = [...checked.value];
    if (strict.value) request.strict = true;
    if (lstSuggest.value) request.lstSuggest = true;
    if (copyAssets.value) request.copyAssets = true;
    if (preset.value) request.preset = preset.value;
    if (listRelation.value) request.listRelation = listRelation.value;
    if (maxEntries.value > 0) request.maxEntries = maxEntries.value;
    return request;
  }

  function refresh(): Promise<void> {
    const token = ++generation;
    if (visible.value) void loadCatalogStatus();
    preview.value = null;
    error.value = "";
    preparing.value = visible.value && allSeeds.value.length > 0;
    if (!preparing.value) {
      if (visible.value) {
        // 空种子不是出错：从工具栏进来就是要贴一份清单，这里给的是引导。用 hint
        // 而不是红色 error——"刚打开的面板"看起来像失败会让人以为功能坏了。
        hint.value = "还没有种子：文件树里选中、编辑器里打开某个文件，或在下面贴清单——条目路径 / 关系名:ID / 纯 ID（先选「清单所属表」）/ 整段 lst 片段行";
        guiding = true;
      }
      return Promise.resolve();
    }
    // 只撤自己那份引导；别的 hint（如"至少保留一个类别"的拒绝理由）不归这里管。
    if (guiding) {
      hint.value = "";
      guiding = false;
    }
    const payload = buildRequest();
    // 串行化：改得很快时，旧请求不能反过来覆盖最新预览（服务端只缓存一份计划）。
    queue = queue.catch(() => {}).then(async () => {
      if (token !== generation || !visible.value) return;
      try {
        const next = await ExtractionService.Prepare(payload);
        if (token !== generation) {
          if (next?.id) await ExtractionService.Release(next.id).catch(() => {});
          return;
        }
        preview.value = next;
      } catch (value: any) {
        if (token === generation) error.value = String(value?.message ?? value);
      } finally {
        if (token === generation) preparing.value = false;
      }
    });
    return queue;
  }

  /** 手输框只在被接受时参与准备：逐字符重取闭包没有意义。 */
  function acceptExtraSeeds(): void {
    const parsed = parseExtraSeeds(extraSeeds.value);
    if (!parsed.length) return;
    seeds.value = [...new Set([...seeds.value, ...parsed])];
    extraSeeds.value = "";
    void refresh();
  }

  function scheduleRefresh(): void {
    if (!visible.value || running.value) return;
    if (debounce) clearTimeout(debounce);
    // 选项改动会重走整次闭包（大集合上几百毫秒到几秒），所以合并连续输入。
    debounce = setTimeout(() => {
      debounce = null;
      void refresh();
    }, 300);
  }

  function open(seedPaths: string[], label: string): Promise<string> {
    if (visible.value) return Promise.resolve("");
    generation++;
    seeds.value = [...new Set(seedPaths.filter(Boolean))];
    extraSeeds.value = "";
    checked.value = [];
    layout.value = "mirror";
    form.value = "text";
    strict.value = false;
    lstSuggest.value = false;
    copyAssets.value = false;
    preset.value = "";
    listRelation.value = "";
    maxEntries.value = 0;
    void loadPresets();
    // 表清单跟着归档走（每份归档有哪些表、各多少行都不同），所以每次打开都取一遍。
    void loadRelations();
    title.value = label;
    error.value = "";
    hint.value = "";
    visible.value = true;
    const waiting = new Promise<string>((resolve) => { complete = resolve; });
    void refresh();
    return waiting;
  }

  function finish(dir: string): void {
    if (debounce) {
      clearTimeout(debounce);
      debounce = null;
    }
    // 引导话属于这一次会话，别让它活到下一次打开（那时可能压根没显示过）。
    if (guiding) {
      hint.value = "";
      guiding = false;
    }
    generation++;
    releasePreview();
    visible.value = false;
    preparing.value = false;
    running.value = false;
    seeds.value = [];
    extraSeeds.value = "";
    const resolve = complete;
    complete = null;
    resolve?.(dir);
  }

  function close(): void {
    if (!running.value) finish("");
  }

  async function execute(): Promise<void> {
    if (!canExport.value || !preview.value) return;
    running.value = true;
    error.value = "";
    const id = preview.value.id;
    try {
      // 服务端在同一次调用里弹原生目录对话框并落盘；取消返回 null，面板保持打开。
      const written = await ExtractionService.ExecuteDialog(id);
      // 计数不留在面板里：面板随即关闭，落点由调用方提示，与导出面板一致。
      if (written?.dir) finish(written.dir);
    } catch (value: any) {
      const text = String(value?.message ?? value);
      if (!text.toLowerCase().includes("cancel")) {
        // 过期时保留服务端的原话（它比"请重新准备"更有信息量），只把预览作废。
        error.value = text;
        if (text.includes("过期") || text.includes("不存在")) {
          generation++;
          releasePreview();
          preparing.value = false;
        }
      }
    } finally {
      running.value = false;
    }
  }

  function invalidate(force = false): void {
    if (!visible.value || (running.value && !force)) return;
    if (debounce) {
      clearTimeout(debounce);
      debounce = null;
    }
    generation++;
    releasePreview();
    preparing.value = false;
    error.value = "归档或版本已变化，请重新准备提取";
  }

  function toggleCategory(id: string, on: boolean): void {
    if (running.value) return;
    const rows = preview.value?.categories ?? [];
    // "空勾选 = 全部类别"是服务端的约定，所以从全部状态取消第一个时必须先把它
    // 展开成显式列表；否则取消动作得到空数组，读起来仍然是"全部"，勾了等于没勾。
    const next = new Set(checked.value.length ? checked.value : rows.map((row) => row.id));
    if (on) next.add(id);
    else next.delete(id);
    // 一个都不留不是可表达的计划（空数组会被当成全部），所以拦住最后一次取消，
    // 让用户用"取消"按钮退出，而不是拿到一份看似收窄实则全量的产物。
    if (next.size === 0) {
      hint.value = "至少保留一个类别；要整个不导就关闭面板";
      return;
    }
    hint.value = "";
    checked.value = rows.filter((row) => next.has(row.id)).map((row) => row.id);
  }

  async function loadPresets(): Promise<void> {
    if (presets.value.length) return;
    try {
      presets.value = (await ExtractionService.Presets()) ?? [];
    } catch {
      // 预设只是便利项：拿不到就退回让用户自己勾类别，不把面板变成错误页。
      presets.value = [];
    }
  }

  async function loadRelations(): Promise<void> {
    try {
      relations.value = (await ExtractionService.Relations()) ?? [];
    } catch {
      // 表清单拿不到时退回老规则（纯 ID 要唯一归属），面板照常可用。
      relations.value = [];
    }
  }

  async function loadCatalogStatus(): Promise<void> {
    try {
      catalogStatus.value = (await ExtractionService.CatalogStatus()) ?? null;
    } catch {
      // 来源只是说明项；取不到就不显示，不影响提取本身。
      catalogStatus.value = null;
    }
  }

  function applyPreset(id: string): void {
    preset.value = id;
    const chosen = presets.value.find((item) => item.id === id);
    checked.value = [...(chosen?.categories ?? [])];
    scheduleRefresh();
  }

  function selectAllCategories(): void {
    checked.value = [];
  }

  watch([checked, strict, layout, form, maxEntries, lstSuggest, copyAssets, listRelation], scheduleRefresh);
  Events.On("archive:opened", close);
  Events.On("archive:closed", close);
  Events.On("archive:changed", () => invalidate());
  Events.On("archive:reloaded", () => invalidate(true));
  Events.On("version:changed", () => invalidate());
  Events.On("version:committed", () => invalidate());

  return {
    visible, title, seeds, extraSeeds, allSeeds, checked, layout, form, strict, lstSuggest, copyAssets,
    maxEntries, presets, preset, activePreset, applyPreset, preview, preparing, running, error, hint,
    relations, listRelation, relationOptions,
    catalogStatus, catalogSourceLabel,
    selectedCount, canExport,
    open, close, refresh, scheduleRefresh, acceptExtraSeeds, execute, invalidate,
    toggleCategory, selectAllCategories,
  };
});
