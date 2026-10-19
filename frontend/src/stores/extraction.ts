import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import { Events } from "@wailsio/runtime";
import { ExtractionService } from "../../bindings/pvfine/services";
import type { ExtractionPreview, ExtractionPreset, ExtractionRequest } from "../../bindings/pvfine/services/models";

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

/** 手输种子按行/逗号/分号拆开；每段可以是条目路径、`关系名:ID` 或纯 ID。 */
export function parseExtraSeeds(text: string): string[] {
  return text
    .split(/[\n\r,;]+/)
    .map((line) => line.trim())
    .filter(Boolean);
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
  const maxEntries = ref(0);
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
    if (maxEntries.value > 0) request.maxEntries = maxEntries.value;
    return request;
  }

  function refresh(): Promise<void> {
    const token = ++generation;
    preview.value = null;
    error.value = "";
    preparing.value = visible.value && allSeeds.value.length > 0;
    if (!preparing.value) {
      if (visible.value) {
        error.value = "请给出至少一个种子：文件树里选中，或在下面手输条目路径 / 关系名:ID";
      }
      return Promise.resolve();
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
    maxEntries.value = 0;
    void loadPresets();
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

  function applyPreset(id: string): void {
    preset.value = id;
    const chosen = presets.value.find((item) => item.id === id);
    checked.value = [...(chosen?.categories ?? [])];
    scheduleRefresh();
  }

  function selectAllCategories(): void {
    checked.value = [];
  }

  watch([checked, strict, layout, form, maxEntries, lstSuggest, copyAssets], scheduleRefresh);
  Events.On("archive:opened", close);
  Events.On("archive:closed", close);
  Events.On("archive:changed", () => invalidate());
  Events.On("archive:reloaded", () => invalidate(true));
  Events.On("version:changed", () => invalidate());
  Events.On("version:committed", () => invalidate());

  return {
    visible, title, seeds, extraSeeds, allSeeds, checked, layout, form, strict, lstSuggest, copyAssets,
    maxEntries, presets, preset, activePreset, applyPreset, preview, preparing, running, error, hint,
    selectedCount, canExport,
    open, close, refresh, scheduleRefresh, acceptExtraSeeds, execute, invalidate,
    toggleCategory, selectAllCategories,
  };
});
