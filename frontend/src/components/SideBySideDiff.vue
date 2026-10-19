<script setup lang="ts">
/**
 * PVF 归档双栏差异对比与实时合并组件
 * 基于 @codemirror/merge MergeView：
 * - 左侧为本地/目标归档，支持直接编辑、撤销重做与 Ctrl+S 保存；
 * - 右侧为对比/基准归档，只读浏览；
 * - 两栏中间提供段落快捷合入按钮（«）；
 * - 顶部工具栏支持差异段落跳转导航、单文件全量覆盖合入以及未保存状态提示。
 *
 * @author flobit
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NButton, NIcon, NSpin, NTag, NTooltip, useMessage } from "naive-ui";
import {
  ArrowDown20Regular,
  ArrowRedo20Regular,
  ArrowUndo20Regular,
  ArrowUp20Regular,
  ChevronLeft20Regular,
  ChevronRight20Regular,
  Copy20Regular,
  DocumentArrowLeft20Regular,
  Open20Regular,
  Save20Regular,
  TextWrap24Regular,
} from "@vicons/fluent";
import {
  EditorView,
  ViewUpdate,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { Compartment, EditorState, Transaction } from "@codemirror/state";
import { defaultKeymap, history, historyKeymap, redo, redoDepth, undo, undoDepth } from "@codemirror/commands";
import { MergeView } from "@codemirror/merge";
import { ArchiveService } from "../../bindings/pvfine/services";
import type { ArchiveDiffFile } from "../../bindings/pvfine/services/models";
import { useArchiveStore } from "../stores/archive";
import { useArchiveDiffStore } from "../stores/archiveDiff";
import { useEditorStore } from "../stores/editor";
import { pvfHighlighting, pvfLanguage } from "../pvfLanguage";
import { registerUndoHandler } from "../activeUndoBridge";
import { isArchiveDiffShortcutActive } from "../archiveDiff";

const props = defineProps<{
  file: ArchiveDiffFile;
  reverse?: boolean;
  active: boolean;
}>();

const diffStore = useArchiveDiffStore();
const archiveStore = useArchiveStore();
const editorStore = useEditorStore();
const message = useMessage();

const mergeHostRef = ref<HTMLDivElement | null>(null);
let mergeViewInstance: MergeView | null = null;

const chunksCount = ref(0);
const activeChunkIndex = ref(0);
const isSaving = ref(false);

const wrappingCompA = new Compartment();
const wrappingCompB = new Compartment();

const leftScrollbarRef = ref<HTMLDivElement | null>(null);
const rightScrollbarRef = ref<HTMLDivElement | null>(null);
const leftScrollWidth = ref(0);
const rightScrollWidth = ref(0);
const leftHasScroll = ref(false);
const rightHasScroll = ref(false);

let isSyncingFromEditor = false;
let isSyncingFromScrollbar = false;
let resizeObserver: ResizeObserver | null = null;

function updateScrollbarMetrics(): void {
  if (!mergeViewInstance) return;
  const scrollerA = mergeViewInstance.a?.scrollDOM;
  const scrollerB = mergeViewInstance.b?.scrollDOM;

  if (scrollerA) {
    leftScrollWidth.value = scrollerA.scrollWidth;
    leftHasScroll.value = scrollerA.scrollWidth > scrollerA.clientWidth + 2;
    if (leftScrollbarRef.value) {
      leftScrollbarRef.value.scrollLeft = scrollerA.scrollLeft;
    }
  }
  if (scrollerB) {
    rightScrollWidth.value = scrollerB.scrollWidth;
    rightHasScroll.value = scrollerB.scrollWidth > scrollerB.clientWidth + 2;
    if (rightScrollbarRef.value) {
      rightScrollbarRef.value.scrollLeft = scrollerB.scrollLeft;
    }
  }
}

function syncScrollbarsFromEditor(): void {
  if (isSyncingFromScrollbar || !mergeViewInstance) return;
  isSyncingFromEditor = true;
  if (leftScrollbarRef.value && mergeViewInstance.a?.scrollDOM) {
    leftScrollbarRef.value.scrollLeft = mergeViewInstance.a.scrollDOM.scrollLeft;
  }
  if (rightScrollbarRef.value && mergeViewInstance.b?.scrollDOM) {
    rightScrollbarRef.value.scrollLeft = mergeViewInstance.b.scrollDOM.scrollLeft;
  }
  isSyncingFromEditor = false;
}

function onLeftScrollbarScroll(e: Event): void {
  if (isSyncingFromEditor || !mergeViewInstance) return;
  isSyncingFromScrollbar = true;
  const target = e.target as HTMLElement;
  if (mergeViewInstance.a?.scrollDOM) {
    mergeViewInstance.a.scrollDOM.scrollLeft = target.scrollLeft;
  }
  isSyncingFromScrollbar = false;
}

function onRightScrollbarScroll(e: Event): void {
  if (isSyncingFromEditor || !mergeViewInstance) return;
  isSyncingFromScrollbar = true;
  const target = e.target as HTMLElement;
  if (mergeViewInstance.b?.scrollDOM) {
    mergeViewInstance.b.scrollDOM.scrollLeft = target.scrollLeft;
  }
  isSyncingFromScrollbar = false;
}

function applyLineWrapping(enabled: boolean): void {
  diffStore.lineWrapping = enabled;
  if (!mergeViewInstance) return;
  mergeViewInstance.a.dispatch({
    effects: wrappingCompA.reconfigure(enabled ? EditorView.lineWrapping : []),
  });
  mergeViewInstance.b.dispatch({
    effects: wrappingCompB.reconfigure(enabled ? EditorView.lineWrapping : []),
  });
  void nextTick(() => {
    updateScrollbarMetrics();
  });
}

function toggleLineWrapping(): void {
  applyLineWrapping(!diffStore.lineWrapping);
}

function handleEditorWheel(view: EditorView, e: WheelEvent): void {
  if (diffStore.lineWrapping) return;
  let dx = e.deltaX;
  if (!dx && e.shiftKey && e.deltaY) {
    dx = e.deltaY;
  }
  if (dx !== 0 && view.scrollDOM) {
    const maxScroll = view.scrollDOM.scrollWidth - view.scrollDOM.clientWidth;
    if (maxScroll > 0) {
      const nextScroll = Math.max(0, Math.min(maxScroll, view.scrollDOM.scrollLeft + dx));
      if (nextScroll !== view.scrollDOM.scrollLeft) {
        view.scrollDOM.scrollLeft = nextScroll;
        syncScrollbarsFromEditor();
        e.preventDefault();
        e.stopPropagation();
      }
    }
  }
}

function normalizeText(text?: string | null): string {
  if (!text) return "";
  return text.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
}

function formatSize(bytes: number): string {
  if (bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

function copyPath(): void {
  if (!props.file?.path) return;
  void navigator.clipboard.writeText(props.file.path);
  message.success("已复制文件路径");
}

// 方案 B（工作台物理侧锚定）：
// 左栏物理位置恒定为本地当前工作区（先选中的归档，可编辑侧），对应后端原始 ArchiveDiffFile 的 after* 字段；
// 右栏物理位置恒定为外部对比目标（后选中的归档，只读参考侧），对应后端原始 ArchiveDiffFile 的 before* 字段。
// 无论全局对比视角如何切换，左右两栏的物理归属与可编辑权限永远恒定，彻底杜绝底稿取错侧覆盖本地内容。
const localExists = computed(() => props.file?.afterExists ?? false);
const otherExists = computed(() => props.file?.beforeExists ?? false);
const localSize = computed(() => props.file?.afterSize ?? 0);
const otherSize = computed(() => props.file?.beforeSize ?? 0);
const localText = computed(() => props.file?.afterText ?? "");
const otherText = computed(() => props.file?.beforeText ?? "");

const canOpenInEditor = computed(() => {
  return archiveStore.open && !!localExists.value;
});

async function openInEditor(): Promise<void> {
  if (!archiveStore.open || !props.file?.path) return;
  try {
    const nodes = await ArchiveService.ResolveFiles([props.file.path]);
    const target = nodes?.[0];
    if (!target || target.fileIndex < 0) {
      message.warning("当前归档中未找到该文件");
      return;
    }
    await editorStore.openFile(target.fileIndex);
    message.success("已在主编辑器中打开");
  } catch (err: any) {
    message.error(err?.message || "无法在主编辑器中打开");
  }
}

const canUndo = ref(false);
const canRedo = ref(false);

interface ViewportScrollSnapshot {
  scrollTop: number;
  scrollLeft: number;
  leftScrollLeft: number;
  rightScrollLeft: number;
  prevCursor: number;
  docLength: number;
}

let suppressScrollIntoView = false;
let lastHistoryChangeStart: number | null = null;
let restoreRaf1: number | null = null;
let restoreRaf2: number | null = null;

function cancelRestoreRaf(): void {
  if (restoreRaf1 !== null) {
    cancelAnimationFrame(restoreRaf1);
    restoreRaf1 = null;
  }
  if (restoreRaf2 !== null) {
    cancelAnimationFrame(restoreRaf2);
    restoreRaf2 = null;
  }
}

function captureViewportScroll(): ViewportScrollSnapshot {
  const scroller = mergeViewInstance?.dom;
  const viewA = mergeViewInstance?.a;
  const viewB = mergeViewInstance?.b;
  return {
    scrollTop: scroller?.scrollTop ?? 0,
    scrollLeft: scroller?.scrollLeft ?? 0,
    leftScrollLeft: viewA?.scrollDOM?.scrollLeft ?? 0,
    rightScrollLeft: viewB?.scrollDOM?.scrollLeft ?? 0,
    prevCursor: viewA?.state.selection.main.head ?? 0,
    docLength: viewA?.state.doc.length ?? 0,
  };
}

function restoreViewportScroll(snapshot: ViewportScrollSnapshot): void {
  const scroller = mergeViewInstance?.dom;
  const viewA = mergeViewInstance?.a;
  const viewB = mergeViewInstance?.b;
  if (scroller) {
    scroller.scrollTop = snapshot.scrollTop;
    scroller.scrollLeft = snapshot.scrollLeft;
  }
  if (viewA?.scrollDOM) {
    viewA.scrollDOM.scrollLeft = snapshot.leftScrollLeft;
  }
  if (viewB?.scrollDOM) {
    viewB.scrollDOM.scrollLeft = snapshot.rightScrollLeft;
  }
  syncScrollbarsFromEditor();
}

function updateUndoRedoStatus(): void {
  if (!mergeViewInstance?.a) {
    canUndo.value = false;
    canRedo.value = false;
    return;
  }
  const state = mergeViewInstance.a.state;
  canUndo.value = undoDepth(state) > 0;
  canRedo.value = redoDepth(state) > 0;
}

function adjustCursorAfterHistoryAction(
  viewA: EditorView,
  snapshot: ViewportScrollSnapshot
): void {
  const currentCursor = viewA.state.selection.main.head;
  const docLen = viewA.state.doc.length;

  // 若撤销/重做后光标被重置到了 pos 0，而新文档非空：
  // 智能修正光标位置，避免跳回文档首行
  if (currentCursor === 0 && docLen > 0) {
    if (lastHistoryChangeStart !== null && lastHistoryChangeStart > 0) {
      const targetPos = Math.min(lastHistoryChangeStart, docLen);
      viewA.dispatch({ selection: { anchor: targetPos } });
    } else if (snapshot.prevCursor > 0 && snapshot.prevCursor <= docLen) {
      viewA.dispatch({ selection: { anchor: snapshot.prevCursor } });
    }
  }
}

function scheduleViewportRestore(
  snapshot: ViewportScrollSnapshot,
  onDone?: () => void
): void {
  cancelRestoreRaf();
  restoreRaf1 = requestAnimationFrame(() => {
    restoreRaf1 = null;
    restoreViewportScroll(snapshot);
    restoreRaf2 = requestAnimationFrame(() => {
      restoreRaf2 = null;
      restoreViewportScroll(snapshot);
      suppressScrollIntoView = false;
      onDone?.();
    });
  });
}

function executeHistoryAction(action: "undo" | "redo"): boolean {
  if (!mergeViewInstance?.a) return false;
  const viewA = mergeViewInstance.a;

  const snapshot = captureViewportScroll();
  suppressScrollIntoView = true;
  lastHistoryChangeStart = null;

  let success = false;
  try {
    success = action === "undo" ? undo(viewA) : redo(viewA);
  } finally {
    restoreViewportScroll(snapshot);
  }

  if (!success) {
    suppressScrollIntoView = false;
    return false;
  }

  adjustCursorAfterHistoryAction(viewA, snapshot);
  viewA.focus();
  updateUndoRedoStatus();

  // MergeView 的 scheduleMeasure 会在 requestAnimationFrame 重排 spacer 占位块，
  // 此处在连续两帧内重新校准滚动条位置，彻底杜绝回退后的视口跳变
  scheduleViewportRestore(snapshot);

  return true;
}

function performUndo(): boolean {
  return executeHistoryAction("undo");
}

function performRedo(): boolean {
  return executeHistoryAction("redo");
}

function renderRevertControlElement(): HTMLElement {
  const btn = document.createElement("button");
  btn.className = "cm-revert-action-btn";
  btn.title = "合入此段落到本地当前工作区（可按 Ctrl+Z 撤销）";
  btn.setAttribute("aria-label", "合入此段落到本地当前工作区");
  btn.textContent = "«";

  // 在 mousedown 捕获阶段（先于 MergeView 自身的 revertClicked 冒泡监听器触发）：
  // 提前将左栏编辑器的光标聚焦到该段落的起始位置。
  // 这样 CodeMirror history 记录此合入事务时的 startSelection 就是该段落位置，
  // 从而在后续按 Ctrl+Z 撤销时，原生选区能够准确恢复在该段落，绝不跳回 pos 0。
  const prepareRevertSelection = () => {
    if (!mergeViewInstance?.a) return;
    const chunkIdx = Number(btn.dataset.chunk);
    if (!isNaN(chunkIdx) && mergeViewInstance.chunks?.[chunkIdx]) {
      const chunk = mergeViewInstance.chunks[chunkIdx];
      const targetPos = Math.min(chunk.fromA, mergeViewInstance.a.state.doc.length);
      mergeViewInstance.a.dispatch({
        selection: { anchor: targetPos },
      });
      mergeViewInstance.a.focus();
    }
  };

  btn.addEventListener("mousedown", prepareRevertSelection, true);
  btn.addEventListener("click", prepareRevertSelection);
  return btn;
}

function createEditorTheme() {
  return EditorView.theme(
    {
      "&": {
        height: "100%",
        fontSize: "12px",
        color: "var(--pvf-text-primary)",
        backgroundColor: "var(--pvf-surface-code)",
      },
      ".cm-scroller": {
        fontFamily: "'SF Mono', Menlo, Consolas, 'Courier New', monospace",
        lineHeight: "1.55",
        userSelect: "text",
      },
      ".cm-gutters": {
        backgroundColor: "transparent",
        border: "none",
        color: "var(--pvf-editor-gutter-text, var(--pvf-text-muted))",
      },
      ".cm-content": { padding: "8px 0" },
      ".cm-activeLine": { backgroundColor: "var(--pvf-editor-active-line, rgba(255, 255, 255, 0.05))" },
      ".cm-activeLineGutter": { backgroundColor: "var(--pvf-editor-active-line, rgba(255, 255, 255, 0.05))" },
    },
    { dark: true }
  );
}

function handleEditorUpdate(update: ViewUpdate): void {
  updateUndoRedoStatus();
  if (!update.docChanged) return;
  const newText = update.state.doc.toString();
  diffStore.updateDraftText(newText);
  if (mergeViewInstance) {
    chunksCount.value = mergeViewInstance.chunks.length;
  }

  const isUndoOrRedo = update.transactions.some(
    (tr) => tr.isUserEvent("undo") || tr.isUserEvent("redo")
  );
  if (isUndoOrRedo) {
    let firstChange: number | null = null;
    update.changes.iterChanges((_fromA, _toA, fromB) => {
      if (firstChange === null) {
        firstChange = fromB;
      }
    });
    lastHistoryChangeStart = firstChange;
  }

  const isRevert = update.transactions.some((tr) => tr.annotation(Transaction.userEvent) === "revert");
  if (isRevert && mergeViewInstance?.a) {
    mergeViewInstance.a.focus();
  }
}

function destroyMergeView(): void {
  if (mergeViewInstance) {
    mergeViewInstance.a.scrollDOM.removeEventListener("scroll", syncScrollbarsFromEditor);
    mergeViewInstance.b.scrollDOM.removeEventListener("scroll", syncScrollbarsFromEditor);
    mergeViewInstance.destroy();
    mergeViewInstance = null;
  }
}

function buildCommonEditorExtensions(): any[] {
  return [
    lineNumbers(),
    highlightActiveLineGutter(),
    highlightActiveLine(),
    drawSelection(),
    history(),
    pvfLanguage.extension,
    pvfHighlighting,
    createEditorTheme(),
    keymap.of([...defaultKeymap, ...historyKeymap]),
  ];
}

function buildLeftEditorExtensions(commonExtensions: any[]): any[] {
  return [
    ...commonExtensions,
    wrappingCompA.of(diffStore.lineWrapping ? EditorView.lineWrapping : []),
    EditorView.scrollHandler.of(() => {
      if (suppressScrollIntoView) {
        // 阻止 CodeMirror 撤销/重做期间的 scrollIntoView 粗暴重置统一视口垂直滚动条
        return true;
      }
      return false;
    }),
    keymap.of([
      {
        key: "Mod-z",
        run: () => performUndo(),
        preventDefault: true,
      },
      {
        key: "Mod-y",
        run: () => performRedo(),
        preventDefault: true,
      },
      {
        key: "Mod-Shift-z",
        run: () => performRedo(),
        preventDefault: true,
      },
      {
        key: "Mod-s",
        run: () => {
          void handleSave();
          return true;
        },
      },
    ]),
    EditorView.updateListener.of((u) => {
      handleEditorUpdate(u);
      if (u.docChanged || u.geometryChanged) {
        updateScrollbarMetrics();
      }
    }),
  ];
}

function buildRightEditorExtensions(commonExtensions: any[]): any[] {
  return [
    ...commonExtensions,
    wrappingCompB.of(diffStore.lineWrapping ? EditorView.lineWrapping : []),
    EditorState.readOnly.of(true),
    EditorView.editable.of(false),
    EditorView.updateListener.of((u) => {
      if (u.geometryChanged) {
        updateScrollbarMetrics();
      }
    }),
  ];
}

let currentInitializedPath = "";

function initMergeView(preserveState = false): void {
  let savedSnapshot: ViewportScrollSnapshot | null = null;
  if (preserveState && mergeViewInstance) {
    savedSnapshot = captureViewportScroll();
  }

  destroyMergeView();
  if (!mergeHostRef.value || !props.file || !props.file.textAvailable) {
    currentInitializedPath = "";
    return;
  }

  currentInitializedPath = props.file.path;
  const baseDoc = normalizeText(diffStore.draftText ?? localText.value ?? "");
  const targetDoc = normalizeText(otherText.value || "");
  const commonExtensions = buildCommonEditorExtensions();

  mergeViewInstance = new MergeView({
    a: {
      doc: baseDoc,
      extensions: buildLeftEditorExtensions(commonExtensions),
    },
    b: {
      doc: targetDoc,
      extensions: buildRightEditorExtensions(commonExtensions),
    },
    parent: mergeHostRef.value,
    revertControls: "b-to-a",
    renderRevertControl: renderRevertControlElement,
    gutter: true,
    highlightChanges: true,
    orientation: "a-b",
  });

  mergeViewInstance.a.scrollDOM.addEventListener("scroll", syncScrollbarsFromEditor);
  mergeViewInstance.b.scrollDOM.addEventListener("scroll", syncScrollbarsFromEditor);
  mergeViewInstance.a.dom.addEventListener("wheel", (e) => {
    if (mergeViewInstance) handleEditorWheel(mergeViewInstance.a, e);
  }, { passive: false });
  mergeViewInstance.b.dom.addEventListener("wheel", (e) => {
    if (mergeViewInstance) handleEditorWheel(mergeViewInstance.b, e);
  }, { passive: false });

  chunksCount.value = mergeViewInstance.chunks.length;
  activeChunkIndex.value = 0;

  if (savedSnapshot) {
    const docLenA = mergeViewInstance.a.state.doc.length;
    const safeCursor = Math.min(savedSnapshot.prevCursor, docLenA);
    mergeViewInstance.a.dispatch({
      selection: { anchor: safeCursor },
    });
    suppressScrollIntoView = true;
    restoreViewportScroll(savedSnapshot);

    scheduleViewportRestore(savedSnapshot);
  }

  void nextTick(() => {
    updateScrollbarMetrics();
    updateUndoRedoStatus();
  });
}

async function handleSave(): Promise<void> {
  if (!props.file?.path || !diffStore.isDraftDirty) return;
  isSaving.value = true;
  const snapshot = captureViewportScroll();
  try {
    const success = await diffStore.mergeText(props.file.path, diffStore.draftText);
    if (success) {
      message.success("修改已成功保存并同步到归档");
      if (mergeViewInstance?.a) {
        mergeViewInstance.a.focus();
        restoreViewportScroll(snapshot);
      }
    } else {
      message.error("保存修改失败");
    }
  } catch (err: any) {
    message.error(`保存失败: ${err?.message ?? err}`);
  } finally {
    isSaving.value = false;
  }
}

function handleMergeCurrentFile(): void {
  if (!mergeViewInstance || !props.file?.path) return;
  const targetDoc = normalizeText(otherText.value || "");
  const currentDoc = mergeViewInstance.a.state.doc.toString();
  if (currentDoc === targetDoc) {
    message.info("当前文件内容已与对比目标一致");
    return;
  }

  // 记录当前有效光标位置，避免全量合入时 selection 变为 0
  const currentCursor = mergeViewInstance.a.state.selection.main.head;
  const safeCursor = Math.min(currentCursor, targetDoc.length);

  // 通过 CodeMirror dispatch 执行整文件替换，作为一条可撤销的历史记录记入 undo 栈
  mergeViewInstance.a.dispatch({
    changes: { from: 0, to: mergeViewInstance.a.state.doc.length, insert: targetDoc },
    selection: { anchor: safeCursor },
    userEvent: "revert",
  });
  mergeViewInstance.a.focus();
  updateUndoRedoStatus();
  message.success("已将对比目标内容全量合入到本地草稿（可按 Ctrl+Z 撤销，按 Ctrl+S 保存）");
}

function scrollToChunk(index: number): void {
  if (!mergeViewInstance || mergeViewInstance.chunks.length === 0) return;
  const total = mergeViewInstance.chunks.length;
  const clamped = ((index % total) + total) % total;
  activeChunkIndex.value = clamped;
  const chunk = mergeViewInstance.chunks[clamped];
  if (!chunk) return;

  const docLenA = mergeViewInstance.a.state.doc.length;
  const targetPosA = Math.min(chunk.fromA, docLenA);

  // 1. 设置左侧可编辑区光标选区，并利用 EditorView.scrollIntoView 将视野精准垂直居中
  mergeViewInstance.a.dispatch({
    selection: { anchor: targetPosA },
    effects: EditorView.scrollIntoView(targetPosA, { y: "center" }),
  });

  // 2. 聚焦至左侧编辑器，激活光标闪烁
  mergeViewInstance.a.focus();

  // 3. 右侧对比目标只读区同步垂直居中定位至对应差异位置
  const docLenB = mergeViewInstance.b.state.doc.length;
  const targetPosB = Math.min(chunk.fromB, docLenB);
  mergeViewInstance.b.dispatch({
    effects: EditorView.scrollIntoView(targetPosB, { y: "center" }),
  });
}

function nextDifference(): void {
  if (!mergeViewInstance || mergeViewInstance.chunks.length === 0) return;
  const total = mergeViewInstance.chunks.length;
  if (total <= 1) {
    scrollToChunk(0);
    return;
  }
  // 严格在当前文件内部切换至下一个差异段落，绝对不跨文件
  const nextIdx = (activeChunkIndex.value + 1) % total;
  scrollToChunk(nextIdx);
}

function previousDifference(): void {
  if (!mergeViewInstance || mergeViewInstance.chunks.length === 0) return;
  const total = mergeViewInstance.chunks.length;
  if (total <= 1) {
    scrollToChunk(0);
    return;
  }
  // 严格在当前文件内部切换至上一个差异段落，绝对不跨文件
  const prevIdx = (activeChunkIndex.value - 1 + total) % total;
  scrollToChunk(prevIdx);
}

function handleGlobalKeydown(e: KeyboardEvent): void {
  if (!mergeViewInstance || e.defaultPrevented) return;

  const isMod = e.ctrlKey || e.metaKey;
  const isAlt = e.altKey;
  const code = e.code;
  const key = e.key ? e.key.toLowerCase() : "";

  if (!isArchiveDiffShortcutActive(props.active, document.activeElement, mergeViewInstance.dom)) return;

  // 0. 差异段落导航：Alt + Up / Alt + Down（仅限当前文件内部，文件间切换由 CompareWorkbench 统一处理）
  if (isAlt && !isMod) {
    if (code === "ArrowUp") {
      e.preventDefault();
      e.stopPropagation();
      previousDifference();
      return;
    }
    if (code === "ArrowDown") {
      e.preventDefault();
      e.stopPropagation();
      nextDifference();
      return;
    }
  }

  // 1. IDEA 差异段落导航：F7（下一个差异） / Shift + F7（上一个差异）
  if (code === "F7") {
    e.preventDefault();
    e.stopPropagation();
    if (e.shiftKey) {
      previousDifference();
    } else {
      nextDifference();
    }
    return;
  }

  // 2. Ctrl+S 保存
  if (isMod && (code === "KeyS" || key === "s")) {
    e.preventDefault();
    e.stopPropagation();
    if (diffStore.isDraftDirty) {
      void handleSave();
    }
    return;
  }

  // 3. Ctrl+Z 撤销 (或 Ctrl+Shift+Z 重做)
  if (isMod && (code === "KeyZ" || key === "z")) {
    e.preventDefault();
    e.stopPropagation();
    if (e.shiftKey) {
      performRedo();
    } else {
      performUndo();
    }
    return;
  }

  // 4. Ctrl+Y 重做
  if (isMod && (code === "KeyY" || key === "y") && !e.shiftKey) {
    e.preventDefault();
    e.stopPropagation();
    performRedo();
    return;
  }
}

watch(
  () => [props.file?.path, props.file?.beforeText, props.file?.afterText],
  ([newPath]) => {
    if (!newPath) {
      currentInitializedPath = "";
      destroyMergeView();
      return;
    }

    const isSameFile = newPath === currentInitializedPath && !!mergeViewInstance;

    if (isSameFile && mergeViewInstance) {
      // 检查当前双栏编辑器中文档是否已经与目标数据完全一致
      const curDocA = normalizeText(mergeViewInstance.a.state.doc.toString());
      const curDocB = normalizeText(mergeViewInstance.b.state.doc.toString());
      const targetDocA = normalizeText(diffStore.draftText ?? localText.value ?? "");
      const targetDocB = normalizeText(otherText.value ?? "");

      if (curDocA === targetDocA && curDocB === targetDocB) {
        // 文档内容完全无变动（如合入后点击保存，编辑器文本即为保存后文本）：
        // 彻底无需销毁重建 MergeView，滚动条与光标 100% 原位保持！
        chunksCount.value = mergeViewInstance.chunks.length;
        updateUndoRedoStatus();
        updateScrollbarMetrics();
        return;
      }
    }

    void nextTick(() => {
      initMergeView(isSameFile);
    });
  },
  { immediate: true }
);

let unregisterUndoHandler: (() => void) | null = null;

onMounted(() => {
  unregisterUndoHandler = registerUndoHandler({
    undo: () => performUndo(),
    redo: () => performRedo(),
    priority: 100,
    isActive: () => {
      return isArchiveDiffShortcutActive(props.active, document.activeElement, mergeViewInstance?.dom ?? null);
    },
  });
  window.addEventListener("keydown", handleGlobalKeydown, true);
  if (mergeHostRef.value && typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(() => {
      updateScrollbarMetrics();
    });
    resizeObserver.observe(mergeHostRef.value);
  }
});

onBeforeUnmount(() => {
  cancelRestoreRaf();
  if (unregisterUndoHandler) {
    unregisterUndoHandler();
    unregisterUndoHandler = null;
  }
  window.removeEventListener("keydown", handleGlobalKeydown, true);
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  destroyMergeView();
});
</script>

<template>
  <div class="side-by-side-diff-container">
    <div class="side-diff-toolbar">
      <div class="toolbar-side toolbar-side--left">
        <span class="column-badge column-badge--local" title="本地当前工作区 (可编辑侧)">本地当前</span>
        <span class="file-path-text" :title="props.file.path">{{ props.file.path }}</span>
        <span
          :class="['file-size-badge', { 'file-size-badge--missing': !localExists }]"
          :title="localExists ? `文件大小: ${formatSize(localSize)}` : '本地当前工作区未包含该文件'"
        >
          {{ localExists ? formatSize(localSize) : '本地未包含' }}
        </span>

        <div class="side-actions-group">
          <NTooltip v-if="canOpenInEditor" trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn"
                aria-label="在主编辑器中打开此文件"
                @click="openInEditor"
              >
                <NIcon size="14"><Open20Regular /></NIcon>
              </button>
            </template>
            在主编辑器新 Tab 中打开
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <button class="icon-tool-btn" aria-label="复制路径" @click="copyPath">
                <NIcon size="13"><Copy20Regular /></NIcon>
              </button>
            </template>
            复制文件路径
          </NTooltip>
        </div>

        <div class="history-actions-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn"
                :disabled="!canUndo"
                aria-label="撤销修改 (Ctrl+Z)"
                @click="performUndo"
              >
                <NIcon size="13"><ArrowUndo20Regular /></NIcon>
              </button>
            </template>
            撤销修改 (Ctrl+Z)
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn"
                :disabled="!canRedo"
                aria-label="重做修改 (Ctrl+Y / Ctrl+Shift+Z)"
                @click="performRedo"
              >
                <NIcon size="13"><ArrowRedo20Regular /></NIcon>
              </button>
            </template>
            重做修改 (Ctrl+Y / Ctrl+Shift+Z)
          </NTooltip>
        </div>

        <div v-if="diffStore.isDraftDirty" class="save-status-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn icon-tool-btn--save"
                :disabled="isSaving"
                aria-label="保存修改到归档 (Ctrl+S)"
                @click="handleSave"
              >
                <span class="dirty-dot">●</span>
                <NIcon size="13"><Save20Regular /></NIcon>
              </button>
            </template>
            草稿已修改，点击保存同步到归档 (Ctrl+S)
          </NTooltip>
        </div>
      </div>

      <div class="toolbar-center">
        <!-- 文件级导航：查看上一个差异文件 / 查看下一个差异文件 -->
        <div class="file-navigation-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="nav-arrow-btn"
                :disabled="!diffStore.hasPreviousFile"
                aria-label="查看上一个差异文件 (Alt+Left)"
                @click="diffStore.openPreviousFile()"
              >
                <NIcon size="13"><ChevronLeft20Regular /></NIcon>
              </button>
            </template>
            查看上一个差异文件 (Alt+Left){{ diffStore.fileNavIndicator ? ` [${diffStore.fileNavIndicator}]` : '' }}
          </NTooltip>

          <span class="file-indicator" :title="`差异文件列表位置: ${diffStore.fileNavIndicator}`">
            {{ diffStore.fileNavIndicator || "文件" }}
          </span>

          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="nav-arrow-btn"
                :disabled="!diffStore.hasNextFile"
                aria-label="查看下一个差异文件 (Alt+Right)"
                @click="diffStore.openNextFile()"
              >
                <NIcon size="13"><ChevronRight20Regular /></NIcon>
              </button>
            </template>
            查看下一个差异文件 (Alt+Right){{ diffStore.fileNavIndicator ? ` [${diffStore.fileNavIndicator}]` : '' }}
          </NTooltip>
        </div>

        <div class="toolbar-divider" />

        <div v-if="chunksCount > 0" class="chunk-navigation-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button class="nav-arrow-btn" aria-label="上一个差异 (Shift+F7 / Alt+Up)" @click="previousDifference">
                <NIcon size="13"><ArrowUp20Regular /></NIcon>
              </button>
            </template>
            上一个差异 (Shift+F7 / Alt+Up)
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <span class="chunk-indicator">{{ activeChunkIndex + 1 }} / {{ chunksCount }}</span>
            </template>
            差异段落 {{ activeChunkIndex + 1 }} / {{ chunksCount }}
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <button class="nav-arrow-btn" aria-label="下一个差异 (F7 / Alt+Down)" @click="nextDifference">
                <NIcon size="13"><ArrowDown20Regular /></NIcon>
              </button>
            </template>
            下一个差异 (F7 / Alt+Down)
          </NTooltip>
        </div>
        <div v-else class="no-diff-badge" title="两份归档中该文件内容完全一致">
          <span>0 / 0</span>
        </div>

        <NTooltip trigger="hover">
          <template #trigger>
            <button
              class="icon-tool-btn icon-tool-btn--merge"
              :disabled="isSaving || diffStore.merging"
              aria-label="合入整文件"
              @click="handleMergeCurrentFile"
            >
              <NIcon size="14"><DocumentArrowLeft20Regular /></NIcon>
            </button>
          </template>
          以对比目标为准，全量合入到本地草稿（可按 Ctrl+Z 撤销，Ctrl+S 保存）
        </NTooltip>

        <NTooltip trigger="hover">
          <template #trigger>
            <button
              class="icon-tool-btn icon-tool-btn--wrap"
              :class="{ 'icon-tool-btn--active': diffStore.lineWrapping }"
              :aria-pressed="diffStore.lineWrapping"
              :aria-label="diffStore.lineWrapping ? '关闭自动换行' : '开启自动换行'"
              @click="toggleLineWrapping"
            >
              <NIcon size="14"><TextWrap24Regular /></NIcon>
            </button>
          </template>
          {{ diffStore.lineWrapping ? '关闭自动换行' : '开启自动换行（超长行可能导致卡顿）' }}
        </NTooltip>
      </div>

      <div class="toolbar-side toolbar-side--right">
        <span class="column-badge column-badge--target" title="外部对比目标 (只读参考侧)">对比目标</span>
        <span class="file-path-text" :title="props.file.path">{{ props.file.path }}</span>
        <span
          :class="['file-size-badge', { 'file-size-badge--missing': !otherExists }]"
          :title="otherExists ? `文件大小: ${formatSize(otherSize)}` : '外部对比目标未包含该文件'"
        >
          {{ otherExists ? formatSize(otherSize) : '目标未包含' }}
        </span>

        <div class="side-actions-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button class="icon-tool-btn" aria-label="复制路径" @click="copyPath">
                <NIcon size="13"><Copy20Regular /></NIcon>
              </button>
            </template>
            复制文件路径
          </NTooltip>
        </div>
      </div>
    </div>

    <div ref="mergeHostRef" class="merge-editor-host" />

    <!-- 底部双栏视口水平滚动条（未换行且内容超宽时显示） -->
    <div
      v-show="!diffStore.lineWrapping && (leftHasScroll || rightHasScroll)"
      class="side-diff-bottom-scrollbars"
      role="region"
      aria-label="双栏水平滚动条"
    >
      <div
        ref="leftScrollbarRef"
        class="diff-h-scrollbar-track diff-h-scrollbar-track--left"
        :class="{ 'is-scrollable': leftHasScroll }"
        @scroll="onLeftScrollbarScroll"
      >
        <div class="diff-h-scrollbar-spacer" :style="{ width: `${leftScrollWidth}px` }" />
      </div>

      <div class="diff-h-scrollbar-divider" />

      <div
        ref="rightScrollbarRef"
        class="diff-h-scrollbar-track diff-h-scrollbar-track--right"
        :class="{ 'is-scrollable': rightHasScroll }"
        @scroll="onRightScrollbarScroll"
      >
        <div class="diff-h-scrollbar-spacer" :style="{ width: `${rightScrollWidth}px` }" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.side-by-side-diff-container {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--pvf-surface-code);
  overflow: hidden;
}

.side-diff-toolbar {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  gap: 12px;
  padding: 0 12px;
  height: 36px;
  min-height: 36px;
  max-height: 36px;
  background: var(--pvf-surface-subtle);
  border-bottom: 1px solid var(--pvf-border-subtle);
  font-size: 12px;
  user-select: none;
  box-sizing: border-box;
  overflow: hidden;
}

.toolbar-side {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
  height: 100%;
}

.toolbar-side--right {
  justify-content: flex-end;
}

.toolbar-center {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex-shrink: 0;
  white-space: nowrap;
  height: 100%;
}

.column-badge {
  font-size: 11px;
  font-weight: 600;
  padding: 1px 6px;
  height: 20px;
  line-height: 18px;
  border-radius: 4px;
  flex-shrink: 0;
  white-space: nowrap;
  box-sizing: border-box;
}

.column-badge--local {
  background: rgba(46, 160, 67, 0.15);
  color: var(--pvf-success-hover, #3fb950);
  border: 1px solid rgba(46, 160, 67, 0.3);
}

.column-badge--target {
  background: rgba(31, 111, 235, 0.15);
  color: #58a6ff;
  border: 1px solid rgba(31, 111, 235, 0.3);
}

.file-path-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 0 1 auto;
  min-width: 0;
}

.file-size-badge {
  font-size: 11px;
  color: var(--pvf-text-muted);
  flex-shrink: 0;
  white-space: nowrap;
}

.file-size-badge--missing {
  color: var(--pvf-text-muted);
  opacity: 0.75;
  font-style: italic;
}

.side-actions-group {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  white-space: nowrap;
}

.history-actions-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  white-space: nowrap;
}

.save-status-group {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  white-space: nowrap;
}

.dirty-dot {
  color: #ffc53d;
  font-size: 11px;
  line-height: 1;
}

.dirty-badge {
  color: #e3b341;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
  flex-shrink: 0;
}

.action-pill-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 0 7px;
  height: 22px;
  line-height: 20px;
  border-radius: 4px;
  border: 1px solid transparent;
  font-size: 11px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  user-select: none;
  white-space: nowrap !important;
  flex-shrink: 0 !important;
  box-sizing: border-box;
}

.action-pill-btn span {
  white-space: nowrap !important;
}

.action-pill-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.action-pill-btn--save {
  background: #238636;
  color: #ffffff;
  border-color: rgba(255, 255, 255, 0.15);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
}

.action-pill-btn--save:hover:not(:disabled) {
  background: #2ea043;
}

.action-pill-btn--merge-file {
  background: var(--pvf-surface-card);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-normal);
}

.action-pill-btn--merge-file:hover:not(:disabled) {
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-strong);
}

@media (max-width: 900px) {
  .file-size-badge {
    display: none;
  }
}

.file-navigation-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 4px;
  padding: 1px 4px;
}

.file-indicator {
  font-size: 11px;
  color: var(--pvf-text-secondary);
  padding: 0 4px;
  font-variant-numeric: tabular-nums;
  font-weight: 500;
}

.toolbar-divider {
  width: 1px;
  height: 16px;
  background: var(--pvf-border-subtle);
  margin: 0 2px;
  flex-shrink: 0;
}

.chunk-navigation-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 4px;
  padding: 1px 4px;
}

.nav-arrow-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  background: transparent;
  color: var(--pvf-text-secondary);
  border-radius: 3px;
  cursor: pointer;
}

.nav-arrow-btn:hover {
  background: var(--pvf-surface-hover);
  color: var(--pvf-text-primary);
}

.chunk-indicator {
  font-size: 11px;
  color: var(--pvf-text-secondary);
  padding: 0 4px;
  font-variant-numeric: tabular-nums;
}

.no-diff-badge {
  font-size: 11px;
  color: var(--pvf-text-muted);
  padding: 2px 6px;
}

.icon-tool-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  background: transparent;
  border: 1px solid transparent;
  color: var(--pvf-text-muted);
  cursor: pointer;
  border-radius: 3px;
  flex-shrink: 0;
  box-sizing: border-box;
  transition: all 0.15s ease;
}

.icon-tool-btn:hover:not(:disabled) {
  color: var(--pvf-text-primary);
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-subtle);
}

.icon-tool-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.icon-tool-btn--save {
  background: rgba(46, 160, 67, 0.2);
  color: #3fb950;
  border-color: rgba(46, 160, 67, 0.4);
  gap: 2px;
  width: auto;
  padding: 0 4px;
}

.icon-tool-btn--save:hover:not(:disabled) {
  background: #238636;
  color: #ffffff;
  border-color: rgba(255, 255, 255, 0.2);
}

.icon-tool-btn--merge {
  background: var(--pvf-surface-card);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-normal);
}

.icon-tool-btn--merge:hover:not(:disabled) {
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-strong);
}

.merge-editor-host {
  flex: 1 1 0%;
  min-height: 0;
  min-width: 0;
  width: 100%;
  height: 100%;
  position: relative;
  overflow: hidden;
}

:deep(.cm-mergeView) {
  height: 100% !important;
  max-height: 100% !important;
  width: 100%;
  overflow-y: auto !important;
  overflow-x: auto !important;
  box-sizing: border-box;
}

:deep(.cm-mergeViewEditors) {
  min-height: 100%;
  display: flex;
  align-items: stretch;
}

:deep(.cm-mergeViewEditor) {
  flex: 1 1 0%;
  min-width: 0;
  overflow: hidden;
}

:deep(.cm-mergeViewEditor:first-child) {
  border-right: 1px solid var(--pvf-border-subtle);
}

:deep(.cm-editor) {
  box-sizing: border-box;
}

:deep(.cm-scroller) {
  font-family: 'SF Mono', Menlo, Consolas, 'Courier New', monospace;
  line-height: 1.55;
  user-select: text;
}

:deep(.cm-merge-revert) {
  width: 28px !important;
  min-width: 28px !important;
  position: relative !important;
  flex-grow: 0 !important;
  flex-shrink: 0 !important;
  background: var(--pvf-surface-subtle) !important;
  border-left: 1px solid var(--pvf-border-subtle) !important;
  border-right: 1px solid var(--pvf-border-subtle) !important;
  user-select: none;
}

:deep(.cm-merge-revert button),
:deep(.cm-revert-action-btn) {
  position: absolute;
  left: 3px;
  width: 22px;
  height: 20px;
  padding: 0;
  margin: 0;
  border: 1px solid rgba(46, 160, 67, 0.4);
  border-radius: 4px;
  background: rgba(46, 160, 67, 0.2);
  color: var(--pvf-success-hover, #3fb950);
  font-size: 14px;
  font-weight: bold;
  line-height: 18px;
  text-align: center;
  cursor: pointer;
  transition: all 0.15s ease;
  z-index: 10;
}

:deep(.cm-merge-revert button:hover),
:deep(.cm-revert-action-btn:hover) {
  background: var(--pvf-success-hover, #3fb950);
  color: #ffffff;
  border-color: var(--pvf-success-hover, #3fb950);
  transform: scale(1.08);
}

.icon-tool-btn--active {
  background: rgba(24, 160, 88, 0.18) !important;
  color: var(--pvf-primary, #18a058) !important;
  border-color: rgba(24, 160, 88, 0.4) !important;
}

.icon-tool-btn--active:hover {
  background: rgba(24, 160, 88, 0.28) !important;
}

.side-diff-bottom-scrollbars {
  display: flex;
  align-items: stretch;
  height: 12px;
  min-height: 12px;
  max-height: 12px;
  background: var(--pvf-surface-subtle);
  border-top: 1px solid var(--pvf-border-subtle);
  flex-shrink: 0;
  box-sizing: border-box;
  overflow: hidden;
  user-select: none;
  z-index: 5;
}

.diff-h-scrollbar-track {
  flex: 1 1 0%;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  height: 100%;
}

.diff-h-scrollbar-track:not(.is-scrollable) {
  overflow-x: hidden;
}

.diff-h-scrollbar-track::-webkit-scrollbar {
  height: 10px;
}

.diff-h-scrollbar-track::-webkit-scrollbar-track {
  background: transparent;
}

.diff-h-scrollbar-track::-webkit-scrollbar-thumb {
  background: var(--pvf-border-normal, rgba(255, 255, 255, 0.2));
  border-radius: 5px;
}

.diff-h-scrollbar-track::-webkit-scrollbar-thumb:hover {
  background: var(--pvf-border-focus, rgba(255, 255, 255, 0.4));
}

.diff-h-scrollbar-spacer {
  height: 1px;
}

.diff-h-scrollbar-divider {
  width: 28px;
  min-width: 28px;
  flex-shrink: 0;
  background: var(--pvf-surface-subtle);
  border-left: 1px solid var(--pvf-border-subtle);
  border-right: 1px solid var(--pvf-border-subtle);
}
</style>
