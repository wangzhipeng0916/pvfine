<script setup lang="ts">
import { computed, h, nextTick, ref, watch } from "vue";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDataTable,
  NIcon,
  NModal,
  NSelect,
  NSpace,
  NTabPane,
  NTabs,
  NText,
  useMessage,
  type DataTableColumns,
  type DataTableInst,
} from "naive-ui";
import {
  ArrowDownload24Regular,
  ArrowImport24Regular,
  Folder20Regular,
  FolderArrowUp24Regular,
  FolderOpen24Regular,
  ImageMultiple20Regular,
} from "@vicons/fluent";
import type {
  ImportCenterEntry,
  ImportListEntry,
  ImportNPKEntry,
} from "../../bindings/pvfine/services/models";
import {
  importFilterOptions,
  importListResolutionOptions,
  importListStateLabel,
  importListStateTagType,
  importNPKStateLabel,
  importNPKStateTagType,
  importSizeLabel,
  importStateLabel,
  importStateTagType,
  importTypeLabel,
  isPrunableNPKState,
  isSelectableListState,
  isWritableState,
  useImportCenterStore,
  type ImportFilter,
  type ImportListResolution,
} from "../stores/importCenter";
import { useArchiveStore } from "../stores/archive";
import { useEditorStore } from "../stores/editor";
import { useExplorerStore } from "../stores/explorer";

const importer = useImportCenterStore();
const archive = useArchiveStore();
const editor = useEditorStore();
const explorer = useExplorerStore();
const message = useMessage();
const tableRef = ref<DataTableInst | null>(null);

// 页签状态放在组件里：store 只关心计划，不关心用户正在看哪一页。
const activeTab = ref("files");

// 归档或版本在面板打开时变化：store 负责关面板，这里只负责把原因说出来。
watch(
  () => importer.notice,
  (text) => {
    if (!text) return;
    message.warning(text);
    importer.consumeNotice();
  },
  { immediate: true },
);

// 换产物后回到文件页：上一份产物的登记表/图片状态已经清空，停在空页签上会让人以为丢了数据。
watch(
  () => importer.preview?.id,
  () => {
    activeTab.value = "files";
  },
);

// 登记表按需加载：切到该页签才发第一次请求；图片页由「比对目标目录」按钮驱动。
watch(
  [activeTab, () => importer.preview?.id],
  ([tab, id]) => {
    if (!id) return;
    if (tab === "lists") importer.ensureList();
  },
  { immediate: true },
);

// 换筛选或换产物后从第一行看起；否则虚拟表格会停在上一份数据的滚动位置。
watch(
  [() => importer.filter, () => importer.preview?.id],
  () => {
    void nextTick(() => tableRef.value?.scrollTo({ top: 0 }));
  },
);

const summaryItems = computed(() => [
  { key: "new", label: "新增", value: importer.counts.new },
  { key: "overwrite", label: "覆盖", value: importer.counts.overwrite },
  { key: "unchanged", label: "未变", value: importer.counts.unchanged },
  { key: "unsupported", label: "不支持", value: importer.counts.unsupported },
  { key: "invalid", label: "无效", value: importer.counts.invalid },
  { key: "selected", label: "已选", value: importer.counts.selected },
]);

const listSummaryItems = computed(() => {
  const counts = importer.listCounts;
  return [
    { key: "new", label: "新增", value: counts.new },
    { key: "registered", label: "已登记", value: counts.registered },
    { key: "id-taken", label: "ID 冲突", value: counts.idTaken },
    { key: "duplicate", label: "路径重复", value: counts.duplicate },
    { key: "dangling", label: "悬空", value: counts.dangling },
    { key: "no-table", label: "表缺失", value: counts.noTable },
    { key: "selected", label: "已选", value: counts.selected },
  ];
});

const npkSummaryItems = computed(() => {
  const counts = importer.npkCounts;
  const items = [
    { key: "containers", label: "容器", value: counts.containers.toLocaleString() },
    { key: "entries", label: "条目", value: counts.entries.toLocaleString() },
    {
      key: "identical",
      label: "可精简",
      value: `${counts.identical.toLocaleString()}（${importSizeLabel(counts.prunedBytes)}）`,
    },
    { key: "different", label: "同名不同内容", value: counts.different.toLocaleString() },
    { key: "missing", label: "目标没有", value: counts.missing.toLocaleString() },
  ];
  // 取消勾选后「可精简」不再等于实际会删掉的数量，补一项避免摘要自相矛盾。
  if (counts.selected !== counts.identical) {
    items.push({ key: "selected", label: "已选", value: counts.selected.toLocaleString() });
  }
  return items;
});

const warnings = computed(() => importer.preview?.warnings ?? []);
const remainingCount = computed(() => Math.max(0, importer.total - importer.loadedCount));
const emptyHint = computed(() => {
  if (importer.error) return "条目加载失败，请查看上方提示";
  if (importer.filter === "all") return importer.loading ? "正在加载条目" : "这份产物没有条目";
  const state = importStateLabel(importer.filter);
  return `当前已加载的 ${importer.loadedCount.toLocaleString()} 行里没有${state}条目`;
});
const listEmptyHint = computed(() => {
  if (importer.listError) return "登记表行加载失败，请查看上方提示";
  if (importer.listLoading) return "正在加载登记表行";
  if (importer.listCounts.total === 0) return "这份产物没有登记表片段（_registries/）";
  return "没有登记表行";
});
const npkEmptyHint = computed(() => {
  if (importer.npkError) return "图片条目加载失败，请查看上方提示";
  if (importer.npkLoading) return "正在比对图片";
  if (importer.npkCounts.entries === 0) return "产物里没有可精简的图片";
  return "没有图片条目";
});

const npkErrorHint = computed(() => {
  if (!importer.npkError.includes("NPK 目录")) return "";
  return "去「设置 → NPK 资源库」选择客户端 ImagePacks2 目录后再试；未配置时无法判断哪些图片是重复的。";
});

const npkActionHint = computed(() => {
  if (importer.npkExecuting) return "正在写出容器，请不要关闭窗口…";
  const counts = importer.npkCounts;
  if (counts.entries === 0) return "产物里没有可精简的图片";
  if (counts.selected === 0) return "没有勾选可精简的条目，输出会保留全部图片";
  return `将精简 ${counts.selected.toLocaleString()} 项，省下 ${importSizeLabel(counts.prunedBytes)}`;
});

const footerHint = computed(() => {
  if (importer.running) return "正在写入归档，请不要关闭窗口…";
  if (!importer.preview) return "预览阶段不会改动归档";
  if (activeTab.value === "npk") return "图片不随执行导入写入；先精简输出，再执行导入";
  const files = importer.counts.selected;
  const rows = importer.listCounts.selected;
  if (files === 0 && rows === 0) return "还没有勾选可写入的条目";
  const parts: string[] = [];
  if (files > 0) parts.push(`${files.toLocaleString()} 个文件`);
  if (rows > 0) parts.push(`${rows.toLocaleString()} 行登记`);
  return `将写入 ${parts.join("、")}`;
});

const listTabHasIssues = computed(() => {
  const counts = importer.listCounts;
  return counts.idTaken + counts.duplicate + counts.dangling + counts.noTable > 0;
});

const columns = computed<DataTableColumns<ImportCenterEntry>>(() => [
  {
    key: "selected",
    width: 40,
    render: (row) => {
      if (!isWritableState(row.state)) {
        // 不可写状态保留一个禁用框，位置与可写行对齐；不显示勾选。
        return h(NCheckbox, {
          checked: false,
          size: "small",
          disabled: true,
          "aria-label": "该条目不可写入",
        });
      }
      return h(NCheckbox, {
        checked: row.selected,
        size: "small",
        disabled: importer.busy,
        "aria-label": `${row.selected ? "取消写入" : "写入"} ${row.archivePath}`,
        "onUpdate:checked": (on: boolean) => importer.setRowSelected(row.archivePath, on),
      });
    },
  },
  {
    key: "archivePath",
    title: "目标路径",
    width: 388,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-path" }, row.archivePath),
  },
  {
    key: "category",
    title: "类别",
    width: 92,
    ellipsis: { tooltip: true },
    render: (row) => (row.category ? h("span", { class: "import-category" }, row.category) : null),
  },
  {
    key: "dataType",
    title: "类型",
    width: 62,
    render: (row) => h("span", { class: "import-type" }, importTypeLabel(row.dataType)),
  },
  {
    key: "size",
    title: "大小",
    width: 84,
    align: "right",
    render: (row) => h("span", { class: "import-size" }, importSizeLabel(row.size)),
  },
  {
    key: "state",
    title: "状态",
    width: 84,
    render: (row) =>
      h(
        "span",
        { class: ["import-badge", `import-badge--${importStateTagType(row.state)}`] },
        importStateLabel(row.state),
      ),
  },
  {
    key: "reason",
    title: "说明",
    width: 150,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-reason" }, row.reason),
  },
]);

const listColumns = computed<DataTableColumns<ImportListEntry>>(() => [
  {
    key: "selected",
    width: 38,
    render: (row) => {
      if (!isSelectableListState(row.state)) {
        // 只有 new / id-taken / duplicate 会写表，其余行保留禁用框对齐位置。
        return h(NCheckbox, {
          checked: false,
          size: "small",
          disabled: true,
          "aria-label": "该登记行不可写入",
        });
      }
      return h(NCheckbox, {
        checked: row.selected,
        size: "small",
        disabled: importer.running,
        "aria-label": `${row.selected ? "取消写入" : "写入"} ${row.listPath} ${row.id}`,
        "onUpdate:checked": (on: boolean) => importer.toggleListRow(row.key, on),
      });
    },
  },
  {
    key: "listPath",
    title: "表",
    width: 152,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-path" }, row.listPath),
  },
  {
    key: "id",
    title: "ID",
    width: 92,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-mono" }, row.id),
  },
  {
    key: "targetPath",
    title: "目标路径",
    width: 250,
    ellipsis: { tooltip: true },
    render: (row) =>
      row.targetPath
        ? h("span", { class: "import-path" }, row.targetPath)
        : h("span", { class: "import-cell-muted" }, "—"),
  },
  {
    key: "state",
    title: "状态",
    width: 84,
    render: (row) =>
      h(
        "span",
        { class: ["import-badge", `import-badge--${importListStateTagType(row.state)}`] },
        importListStateLabel(row.state),
      ),
  },
  {
    key: "conflictOf",
    title: "冲突",
    width: 156,
    ellipsis: { tooltip: true },
    render: (row) =>
      row.conflictOf ? h("span", { class: "import-conflict" }, row.conflictOf) : null,
  },
  {
    key: "resolution",
    title: "消解方式",
    width: 104,
    render: (row) => {
      const options = importListResolutionOptions(row.state);
      if (!options) return h("span", { class: "import-cell-muted" }, "—");
      return h(NSelect, {
        value: row.resolution || "skip",
        size: "small",
        options,
        consistentMenuWidth: false,
        disabled: importer.running,
        "aria-label": `${row.listPath} ${row.id} 的消解方式`,
        "onUpdate:value": (value: string | number | null) =>
          importer.setListResolution(row.key, String(value ?? "skip") as ImportListResolution),
      });
    },
  },
  {
    key: "reason",
    title: "说明",
    width: 150,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-reason" }, row.reason),
  },
]);

const npkColumns = computed<DataTableColumns<ImportNPKEntry>>(() => [
  {
    key: "selected",
    width: 38,
    render: (row) => {
      if (!isPrunableNPKState(row.state)) {
        // 同名不同内容是覆盖、不是重复：不允许勾选删除。
        return h(NCheckbox, {
          checked: false,
          size: "small",
          disabled: true,
          "aria-label": "该图片不可精简",
        });
      }
      return h(NCheckbox, {
        checked: row.selected,
        size: "small",
        disabled: importer.running || importer.npkPlanning || importer.npkExecuting,
        "aria-label": `${row.selected ? "保留" : "精简"} ${row.name}`,
        "onUpdate:checked": (on: boolean) => importer.setNPKSelected(row.key, on),
      });
    },
  },
  {
    key: "container",
    title: "容器",
    width: 130,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-path" }, row.container),
  },
  {
    key: "name",
    title: "图片路径",
    width: 320,
    ellipsis: { tooltip: true },
    render: (row) => h("span", { class: "import-path" }, row.name),
  },
  {
    key: "size",
    title: "大小",
    width: 84,
    align: "right",
    render: (row) => h("span", { class: "import-size" }, importSizeLabel(row.size)),
  },
  {
    key: "targetNpk",
    title: "目标容器",
    width: 140,
    ellipsis: { tooltip: true },
    render: (row) =>
      row.targetNpk
        ? h("span", { class: "import-path" }, row.targetNpk)
        : h("span", { class: "import-cell-muted" }, "—"),
  },
  {
    key: "state",
    title: "状态",
    width: 150,
    render: (row) =>
      h(
        "span",
        { class: ["import-badge", `import-badge--${importNPKStateTagType(row.state)}`] },
        importNPKStateLabel(row.state),
      ),
  },
]);

function rowClassName(row: ImportCenterEntry): string {
  return isWritableState(row.state) && row.selected ? "import-row--selected" : "";
}

function listRowClassName(row: ImportListEntry): string {
  return isSelectableListState(row.state) && row.selected ? "import-row--selected" : "";
}

function npkRowClassName(row: ImportNPKEntry): string {
  return isPrunableNPKState(row.state) && row.selected ? "import-row--selected" : "";
}

function nearBottom(event: Event): boolean {
  const target = event.target as HTMLElement | null;
  // 没有纵向溢出时（行很少或正在加载）不要把横向滚动误判成"到底"。
  if (!target || target.scrollHeight <= target.clientHeight + 4) return false;
  return target.scrollHeight - target.scrollTop - target.clientHeight <= 160;
}

function onTableScroll(event: Event): void {
  if (nearBottom(event)) importer.loadMore();
}

function onListScroll(event: Event): void {
  if (nearBottom(event)) importer.loadListMore();
}

function onNPKScroll(event: Event): void {
  if (nearBottom(event)) importer.loadNPKMore();
}

async function onExecute(): Promise<void> {
  const result = await importer.execute();
  if (!result) return; // 失败信息已经留在模态里。
  // 先报结果再刷新界面：导入已经落盘，刷新慢不该拖住反馈。
  message.success(
    `写入 ${result.written.toLocaleString()} / 覆盖 ${result.overwritten.toLocaleString()} / 登记 ${result.listRows.toLocaleString()} / 跳过 ${result.skipped.toLocaleString()} / 仍缺失 ${result.listDangling.toLocaleString()}`,
  );
  try {
    // 导入改了归档内容：打开中的编辑器要按新索引重绑，否则保存会写回过期内容。
    await editor.refreshAfterArchiveChange(result.changedPaths ?? []);
    await Promise.all([archive.refreshInfo(), explorer.reload()]);
  } catch {
    message.warning("导入已完成，但界面刷新失败，请重新打开归档确认内容");
  }
}

async function onExecuteNPK(): Promise<void> {
  const result = await importer.executeNPK();
  if (!result) return; // 取消目录与失败都不弹成功提示；错误留在页签里。
  message.success(
    `写出 ${result.containers.toLocaleString()} 个容器 / 精简 ${result.pruned.toLocaleString()} 项 / 省下 ${importSizeLabel(result.prunedBytes)}`,
  );
}

function chipDot(state: ImportFilter): string {
  return `import-chip-dot--${state}`;
}
</script>

<template>
  <NModal
    :show="importer.visible"
    preset="card"
    :title="importer.title"
    class="import-center-modal"
    :style="{ width: '980px', maxWidth: 'calc(100vw - 32px)', maxHeight: 'calc(100vh - 48px)' }"
    :content-style="{ overflowY: 'auto', minHeight: 0 }"
    :z-index="3000"
    :closable="!importer.running"
    :mask-closable="!importer.running"
    :close-on-esc="!importer.running"
    @update:show="(show: boolean) => { if (!show) importer.close(); }"
  >
    <NSpace vertical :size="12">
      <NAlert v-if="importer.error" type="error">
        {{ importer.error }}
      </NAlert>

      <div v-if="!importer.preview" class="import-empty">
        <div class="import-empty-icon" aria-hidden="true">
          <NIcon :size="26"><FolderArrowUp24Regular /></NIcon>
        </div>
        <div class="import-empty-heading">把提取产物灌回当前归档</div>
        <p class="import-empty-copy">
          选择一份提取产物目录，里面需要有 manifest.json。勾选的脚本与文本条目会按判定结果写回当前归档；
          二进制条目会被列出，但不会写入。
        </p>
        <NButton
          type="primary"
          size="large"
          :loading="importer.opening"
          :disabled="importer.running"
          @click="importer.chooseDirectory()"
        >
          <template #icon><NIcon><FolderOpen24Regular /></NIcon></template>
          选择提取产物目录
        </NButton>
        <NText depth="3" class="import-empty-note">预览阶段只读归档，确认「执行导入」后才会写入。</NText>
      </div>

      <template v-else>
        <div class="import-root">
          <NIcon class="import-root-icon"><Folder20Regular /></NIcon>
          <span class="import-root-path" :title="importer.preview?.root ?? ''">{{ importer.preview?.root }}</span>
          <NButton
            text
            size="tiny"
            class="import-root-change"
            :loading="importer.opening"
            :disabled="importer.busy"
            @click="importer.chooseDirectory()"
          >
            换一份
          </NButton>
        </div>

        <NAlert v-for="warning in warnings" :key="warning" type="warning">{{ warning }}</NAlert>

        <NTabs v-model:value="activeTab" type="line" :animated="false" pane-style="padding-top: 10px">
          <NTabPane name="files" tab="文件">
            <NSpace vertical :size="10">
              <div class="import-summary" role="group" aria-label="导入摘要">
                <span class="import-summary-total">共 <b>{{ importer.counts.total.toLocaleString() }}</b> 条</span>
                <template v-for="item in summaryItems" :key="item.key">
                  <span class="import-summary-sep" aria-hidden="true">·</span>
                  <span class="import-summary-item" :class="`import-summary-item--${item.key}`">
                    <span class="import-summary-dot" aria-hidden="true" />
                    {{ item.label }} <b>{{ item.value.toLocaleString() }}</b>
                  </span>
                </template>
              </div>

              <div class="import-controls">
                <div class="import-chips" role="group" aria-label="按状态筛选">
                  <button
                    v-for="chip in importFilterOptions"
                    :key="chip.value"
                    type="button"
                    class="import-chip"
                    :class="{ 'import-chip--active': importer.filter === chip.value }"
                    :aria-pressed="importer.filter === chip.value"
                    @click="importer.filter = chip.value"
                  >
                    <span
                      v-if="chip.value !== 'all'"
                      class="import-chip-dot"
                      :class="chipDot(chip.value)"
                      aria-hidden="true"
                    />
                    {{ chip.label }}
                  </button>
                </div>
                <div class="import-bulk">
                  <NButton
                    size="tiny"
                    quaternary
                    :disabled="!importer.canSelectAll"
                    :title="`只提交已加载的 ${importer.loadedCount.toLocaleString()} 行里可写的条目`"
                    @click="importer.selectLoaded(true)"
                  >
                    全选可写
                  </NButton>
                  <span class="import-bulk-sep" aria-hidden="true" />
                  <NButton
                    size="tiny"
                    quaternary
                    :disabled="!importer.canClear"
                    :title="`只提交已加载的 ${importer.loadedCount.toLocaleString()} 行里可写的条目`"
                    @click="importer.selectLoaded(false)"
                  >
                    全不选
                  </NButton>
                </div>
              </div>

              <div class="import-scope">
                <NText depth="3">
                  已加载 {{ importer.loadedCount.toLocaleString() }} / {{ importer.total.toLocaleString() }} 行
                  <template v-if="importer.filter !== 'all'">
                    · 当前筛选 {{ importer.filteredEntries.length.toLocaleString() }} 行
                  </template>
                </NText>
                <NText v-if="importer.hiddenSelectedCount > 0" class="import-scope-note">
                  另有 {{ importer.hiddenSelectedCount.toLocaleString() }} 条尚未加载的条目仍是勾选的
                </NText>
                <NText v-else-if="importer.hasMore" depth="3">全选与全不选只作用于已加载的行</NText>
                <NText v-else depth="3">已全部加载</NText>
              </div>

              <div class="import-table-wrap">
                <NDataTable
                  ref="tableRef"
                  :columns="columns"
                  :data="importer.filteredEntries"
                  :row-key="(row: ImportCenterEntry) => row.archivePath"
                  :row-class-name="rowClassName"
                  :max-height="340"
                  :min-row-height="34"
                  :scroll-x="900"
                  :bordered="false"
                  :loading="importer.loading && importer.loadedCount === 0"
                  single-line
                  virtual-scroll
                  size="small"
                  class="import-table"
                  @scroll="onTableScroll"
                >
                  <template #empty>
                    <div class="import-table-empty">
                      <NText depth="3">{{ emptyHint }}</NText>
                      <NButton
                        v-if="importer.error"
                        text
                        size="tiny"
                        type="primary"
                        :loading="importer.loading"
                        @click="importer.retry()"
                      >
                        重试
                      </NButton>
                      <NButton
                        v-else-if="importer.hasMore"
                        text
                        size="tiny"
                        type="primary"
                        :loading="importer.loading"
                        @click="importer.loadMore()"
                      >
                        加载更多
                      </NButton>
                    </div>
                  </template>
                </NDataTable>
              </div>

              <div class="import-table-foot">
                <NText depth="3">
                  <template v-if="importer.loading">正在加载…</template>
                  <template v-else-if="importer.error && importer.loadedCount === 0">条目加载失败</template>
                  <template v-else-if="importer.hasMore">
                    还有 {{ remainingCount.toLocaleString() }} 行未加载
                  </template>
                  <template v-else>已加载全部 {{ importer.total.toLocaleString() }} 行</template>
                </NText>
                <NButton
                  v-if="importer.hasMore"
                  size="tiny"
                  secondary
                  :loading="importer.loading"
                  :disabled="importer.opening || importer.running || importer.selecting"
                  @click="importer.loadMore()"
                >
                  加载更多
                </NButton>
              </div>
            </NSpace>
          </NTabPane>

          <NTabPane name="lists">
            <template #tab>
              登记表
              <span
                class="import-tab-count"
                :class="{ 'import-tab-count--warning': listTabHasIssues }"
                :title="listTabHasIssues ? '有冲突或缺失的登记行需要处理' : '登记表行数'"
              >
                {{ importer.listCounts.total.toLocaleString() }}
              </span>
            </template>

            <NSpace vertical :size="10">
              <NAlert v-if="importer.listError" type="error">{{ importer.listError }}</NAlert>

              <div class="import-summary" role="group" aria-label="登记表摘要">
                <span class="import-summary-total">共 <b>{{ importer.listCounts.total.toLocaleString() }}</b> 行</span>
                <template v-for="item in listSummaryItems" :key="item.key">
                  <span class="import-summary-sep" aria-hidden="true">·</span>
                  <span class="import-summary-item" :class="`import-summary-item--${item.key}`">
                    <span class="import-summary-dot" aria-hidden="true" />
                    {{ item.label }} <b>{{ item.value.toLocaleString() }}</b>
                  </span>
                </template>
              </div>

              <div class="import-scope">
                <NText depth="3">
                  已加载 {{ importer.listLoadedCount.toLocaleString() }} /
                  {{ importer.listTotal.toLocaleString() }} 行
                </NText>
                <NText v-if="importer.listHasMore" depth="3">滚动到底部或点「加载更多」继续</NText>
                <NText v-else-if="importer.listTotal > 0" depth="3">已全部加载</NText>
              </div>

              <div class="import-table-wrap">
                <NDataTable
                  :columns="listColumns"
                  :data="importer.listEntries"
                  :row-key="(row: ImportListEntry) => row.key"
                  :row-class-name="listRowClassName"
                  :max-height="320"
                  :min-row-height="34"
                  :scroll-x="1040"
                  :bordered="false"
                  :loading="importer.listLoading && importer.listLoadedCount === 0"
                  single-line
                  virtual-scroll
                  size="small"
                  class="import-table"
                  @scroll="onListScroll"
                >
                  <template #empty>
                    <div class="import-table-empty">
                      <NText depth="3">{{ listEmptyHint }}</NText>
                      <NButton
                        v-if="importer.listError"
                        text
                        size="tiny"
                        type="primary"
                        :loading="importer.listLoading"
                        @click="importer.retryList()"
                      >
                        重试
                      </NButton>
                      <NButton
                        v-else-if="importer.listHasMore"
                        text
                        size="tiny"
                        type="primary"
                        :loading="importer.listLoading"
                        @click="importer.loadListMore()"
                      >
                        加载更多
                      </NButton>
                    </div>
                  </template>
                </NDataTable>
              </div>

              <div class="import-table-foot">
                <NText depth="3">
                  <template v-if="importer.listLoading">正在加载…</template>
                  <template v-else-if="importer.listError && importer.listLoadedCount === 0">
                    登记表行加载失败
                  </template>
                  <template v-else-if="importer.listHasMore">
                    还有 {{ importer.listRemaining.toLocaleString() }} 行未加载
                  </template>
                  <template v-else>已加载全部 {{ importer.listTotal.toLocaleString() }} 行</template>
                </NText>
                <NButton
                  v-if="importer.listHasMore"
                  size="tiny"
                  secondary
                  :loading="importer.listLoading"
                  :disabled="importer.running"
                  @click="importer.loadListMore()"
                >
                  加载更多
                </NButton>
              </div>
            </NSpace>
          </NTabPane>

          <NTabPane name="npk" tab="图片">
            <NSpace vertical :size="10">
              <NText depth="3" class="import-npk-order">
                精简需要在执行导入之前完成；目标目录不会被修改，产物写到单独的输出目录。
              </NText>

              <NAlert v-if="importer.npkError" type="error">{{ importer.npkError }}</NAlert>
              <NText v-if="npkErrorHint" depth="3" class="import-npk-hint">{{ npkErrorHint }}</NText>

              <div v-if="!importer.npkPreview" class="import-npk-intro">
                <div class="import-npk-intro-copy">
                  <div class="import-npk-intro-title">
                    把产物 npk/ 里的容器与目标客户端的 ImagePacks2 比对
                  </div>
                  <p class="import-npk-intro-text">
                    只保留宿主机没有的资源：同名同字节的图片会被精简掉，
                    同名不同内容的保留（那是覆盖，不是重复）。目标目录只读，一个字节都不会改。
                  </p>
                </div>
                <NButton
                  type="primary"
                  :loading="importer.npkPlanning"
                  :disabled="importer.running"
                  @click="importer.planNPK()"
                >
                  <template #icon><NIcon><ImageMultiple20Regular /></NIcon></template>
                  {{ importer.npkPlanning ? "正在比对…" : "比对目标目录" }}
                </NButton>
              </div>

              <template v-else>
                <div class="import-root">
                  <NIcon class="import-root-icon"><Folder20Regular /></NIcon>
                  <span class="import-root-path" :title="importer.npkPreview.targetDir">
                    {{ importer.npkPreview.targetDir }}
                  </span>
                  <NButton
                    text
                    size="tiny"
                    class="import-root-change"
                    :loading="importer.npkPlanning"
                    :disabled="importer.npkPlanning || importer.npkExecuting || importer.running"
                    @click="importer.planNPK()"
                  >
                    重新比对
                  </NButton>
                </div>

                <div class="import-summary" role="group" aria-label="图片摘要">
                  <template v-for="(item, index) in npkSummaryItems" :key="item.key">
                    <span v-if="index > 0" class="import-summary-sep" aria-hidden="true">·</span>
                    <span class="import-summary-item" :class="`import-summary-item--${item.key}`">
                      <span class="import-summary-dot" aria-hidden="true" />
                      {{ item.label }} <b>{{ item.value }}</b>
                    </span>
                  </template>
                </div>

                <NAlert
                  v-for="warning in importer.npkPreview.warnings ?? []"
                  :key="warning"
                  type="warning"
                >
                  {{ warning }}
                </NAlert>

                <div class="import-table-wrap">
                  <NDataTable
                    :columns="npkColumns"
                    :data="importer.npkEntries"
                    :row-key="(row: ImportNPKEntry) => row.key"
                    :row-class-name="npkRowClassName"
                    :max-height="320"
                    :min-row-height="34"
                    :scroll-x="880"
                    :bordered="false"
                    :loading="importer.npkLoading && importer.npkLoadedCount === 0"
                    single-line
                    virtual-scroll
                    size="small"
                    class="import-table"
                    @scroll="onNPKScroll"
                  >
                    <template #empty>
                      <div class="import-table-empty">
                        <NText depth="3">{{ npkEmptyHint }}</NText>
                        <NButton
                          v-if="importer.npkError"
                          text
                          size="tiny"
                          type="primary"
                          :loading="importer.npkPlanning"
                          @click="importer.planNPK()"
                        >
                          重新比对
                        </NButton>
                      </div>
                    </template>
                  </NDataTable>
                </div>

                <div class="import-table-foot">
                  <NText depth="3">
                    <template v-if="importer.npkLoading">正在加载…</template>
                    <template v-else-if="importer.npkHasMore">
                      还有 {{ importer.npkRemaining.toLocaleString() }} 条未加载
                    </template>
                    <template v-else>已加载全部 {{ importer.npkTotal.toLocaleString() }} 条</template>
                  </NText>
                  <NButton
                    v-if="importer.npkHasMore"
                    size="tiny"
                    secondary
                    :loading="importer.npkLoading"
                    :disabled="importer.npkPlanning || importer.npkExecuting"
                    @click="importer.loadNPKMore()"
                  >
                    加载更多
                  </NButton>
                </div>

                <div class="import-npk-actions">
                  <NText depth="3">{{ npkActionHint }}</NText>
                  <NButton
                    type="primary"
                    :loading="importer.npkExecuting"
                    :disabled="!importer.canExecuteNPK"
                    @click="onExecuteNPK"
                  >
                    <template #icon><NIcon><ArrowDownload24Regular /></NIcon></template>
                    精简并输出…
                  </NButton>
                </div>
              </template>
            </NSpace>
          </NTabPane>
        </NTabs>
      </template>
    </NSpace>

    <template #footer>
      <div class="import-footer">
        <NText depth="3" class="import-footer-hint">{{ footerHint }}</NText>
        <div class="import-footer-actions">
          <NButton :disabled="importer.running" @click="importer.close()">取消</NButton>
          <NButton
            v-if="importer.preview && activeTab !== 'npk'"
            type="primary"
            :loading="importer.running"
            :disabled="!importer.canExecute"
            @click="onExecute"
          >
            <template #icon><NIcon><ArrowImport24Regular /></NIcon></template>
            执行导入
          </NButton>
        </div>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.import-center-modal :deep(.n-card__content) {
  min-height: 0;
  overflow-y: auto;
}
.import-center-modal :deep(.n-alert) {
  --n-padding: 8px 12px;
}
.import-center-modal :deep(.n-tabs-tab) {
  font-size: 12.5px;
}
.import-center-modal :deep(.n-tabs-pane-wrapper) {
  overflow: visible;
}

.import-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 36px 28px 42px;
  text-align: center;
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  background:
    radial-gradient(120% 90% at 50% 0%, var(--pvf-primary-soft), transparent 62%),
    var(--pvf-surface-inset);
}
.import-empty-icon {
  display: grid;
  place-items: center;
  width: 52px;
  height: 52px;
  margin-bottom: 2px;
  color: var(--pvf-primary);
  background: var(--pvf-primary-soft);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 14px;
}
.import-empty-heading {
  font-size: 16px;
  font-weight: 600;
  letter-spacing: 0.1px;
  color: var(--pvf-text-primary);
}
.import-empty-copy {
  max-width: 480px;
  margin: 0;
  font-size: 12px;
  line-height: 1.8;
  color: var(--pvf-text-secondary);
}
.import-empty-note {
  font-size: 11.5px;
}

.import-root {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.import-root-icon {
  flex: none;
  color: var(--pvf-text-muted);
}
.import-root-path {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  color: var(--pvf-text-secondary);
}
.import-root-change {
  flex: none;
}

.import-tab-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 16px;
  padding: 0 5px;
  margin-left: 6px;
  font-size: 10.5px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  line-height: 14px;
  color: var(--pvf-text-muted);
  background: var(--pvf-surface-subtle);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  transition: color 120ms ease, background 120ms ease;
}
.import-tab-count--warning {
  color: var(--pvf-warning);
  background: var(--pvf-warning-surface);
  border-color: transparent;
}

.import-summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 3px 0;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  background: var(--pvf-surface-subtle);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 6px;
}
.import-summary b {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--pvf-text-primary);
}
.import-summary-total {
  font-weight: 500;
}
.import-summary-total b {
  font-size: 13px;
}
.import-summary-sep {
  margin: 0 6px;
  color: var(--pvf-text-faint);
}
.import-summary-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}
.import-summary-item--selected {
  color: var(--pvf-text-primary);
}
.import-summary-item--selected b {
  color: var(--pvf-primary);
}
.import-summary-dot {
  width: 6px;
  height: 6px;
  flex: none;
  border-radius: 50%;
  background: var(--pvf-text-faint);
}
.import-summary-item--new .import-summary-dot {
  background: var(--pvf-success);
}
.import-summary-item--overwrite .import-summary-dot {
  background: var(--pvf-warning);
}
.import-summary-item--unchanged .import-summary-dot {
  background: var(--pvf-text-faint);
}
.import-summary-item--unsupported .import-summary-dot {
  background: var(--pvf-info);
}
.import-summary-item--invalid .import-summary-dot {
  background: var(--pvf-error);
}
.import-summary-item--selected .import-summary-dot {
  background: var(--pvf-primary);
  box-shadow: 0 0 0 3px var(--pvf-primary-soft);
}
.import-summary-item--registered .import-summary-dot {
  background: var(--pvf-text-faint);
}
.import-summary-item--id-taken .import-summary-dot,
.import-summary-item--duplicate .import-summary-dot {
  background: var(--pvf-warning);
}
.import-summary-item--dangling .import-summary-dot {
  background: var(--pvf-info);
}
.import-summary-item--no-table .import-summary-dot {
  background: var(--pvf-error);
}
.import-summary-item--containers .import-summary-dot {
  background: var(--pvf-primary);
}
.import-summary-item--identical .import-summary-dot {
  background: var(--pvf-success);
}
.import-summary-item--different .import-summary-dot {
  background: var(--pvf-error);
}

.import-controls {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.import-chips {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}
.import-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 24px;
  padding: 0 9px;
  font-family: inherit;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  background: var(--pvf-surface-subtle);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 12px;
  cursor: pointer;
  transition: background 120ms ease, color 120ms ease, border-color 120ms ease;
}
.import-chip:hover {
  color: var(--pvf-text-primary);
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-normal);
}
.import-chip:focus-visible {
  outline: 2px solid var(--pvf-effect-focus-ring);
  outline-offset: 1px;
}
.import-chip--active {
  color: var(--pvf-primary);
  background: var(--pvf-primary-soft);
  border-color: var(--pvf-primary);
  font-weight: 600;
}
.import-chip-dot {
  width: 5px;
  height: 5px;
  flex: none;
  border-radius: 50%;
  background: var(--pvf-text-faint);
}
.import-chip-dot--new {
  background: var(--pvf-success);
}
.import-chip-dot--overwrite {
  background: var(--pvf-warning);
}
.import-chip-dot--unchanged {
  background: var(--pvf-text-faint);
}
.import-chip-dot--unsupported {
  background: var(--pvf-info);
}
.import-chip-dot--invalid {
  background: var(--pvf-error);
}
.import-bulk {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}
.import-bulk-sep {
  width: 1px;
  height: 12px;
  margin: 0 2px;
  background: var(--pvf-border-normal);
}

.import-scope {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
  min-height: 16px;
  font-size: 11.5px;
}
.import-scope-note {
  font-size: 11.5px;
  color: var(--pvf-warning);
}

.import-table-wrap {
  overflow: hidden;
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-normal);
  border-radius: 6px;
}
.import-table :deep(.n-data-table-td) {
  height: 34px;
  padding-top: 0;
  padding-bottom: 0;
}
.import-table :deep(.n-data-table-th) {
  font-size: 11.5px;
  font-weight: 500;
  color: var(--pvf-text-muted);
}
.import-table :deep(.n-data-table-tr.import-row--selected > .n-data-table-td) {
  background-color: var(--pvf-primary-soft);
}
.import-table :deep(.n-data-table-tr:hover > .n-data-table-td) {
  background-color: var(--pvf-surface-hover);
}
.import-table :deep(.n-data-table-tr.import-row--selected:hover > .n-data-table-td) {
  background-color: var(--pvf-primary-selected);
}

.import-path {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  color: var(--pvf-text-primary);
}
.import-mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: var(--pvf-text-primary);
}
.import-cell-muted {
  font-size: 12px;
  color: var(--pvf-text-faint);
}
.import-conflict {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--pvf-warning);
}
.import-category {
  font-size: 12px;
  color: var(--pvf-text-secondary);
}
.import-type {
  font-size: 11.5px;
  color: var(--pvf-text-muted);
}
.import-size {
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
  color: var(--pvf-text-secondary);
}
.import-reason {
  font-size: 11.5px;
  color: var(--pvf-text-muted);
}

.import-badge {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border-radius: 9px;
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  letter-spacing: 0.02em;
  white-space: nowrap;
}
.import-badge--success {
  color: var(--pvf-success);
  background: var(--pvf-success-surface);
}
.import-badge--warning {
  color: var(--pvf-warning);
  background: var(--pvf-warning-surface);
}
.import-badge--default {
  color: var(--pvf-text-muted);
  background: var(--pvf-surface-subtle);
}
.import-badge--info {
  color: var(--pvf-info);
  background: var(--pvf-info-surface);
}
.import-badge--error {
  color: var(--pvf-error);
  background: var(--pvf-error-surface);
}

.import-table-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: 26px 12px;
}

.import-table-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 26px;
}

.import-npk-order {
  display: block;
  padding-left: 8px;
  border-left: 2px solid var(--pvf-border-normal);
  line-height: 1.6;
}
.import-npk-intro {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 20px;
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  background:
    radial-gradient(110% 130% at 0% 0%, var(--pvf-primary-soft), transparent 60%),
    var(--pvf-surface-inset);
}
.import-npk-intro-copy {
  min-width: 0;
}
.import-npk-intro-title {
  margin-bottom: 4px;
  font-size: 13px;
  font-weight: 600;
  color: var(--pvf-text-primary);
}
.import-npk-intro-text {
  max-width: 560px;
  margin: 0;
  font-size: 12px;
  line-height: 1.75;
  color: var(--pvf-text-secondary);
}
.import-npk-intro :deep(.n-button) {
  flex: none;
}
.import-npk-hint {
  display: block;
  line-height: 1.6;
}
.import-npk-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding-top: 2px;
}

.import-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.import-footer-hint {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
}
.import-footer-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}

@media (max-width: 720px) {
  .import-empty {
    padding: 24px 16px 28px;
  }
  .import-footer {
    flex-wrap: wrap;
  }
  .import-npk-intro {
    flex-direction: column;
    align-items: flex-start;
    padding: 14px 16px;
  }
}
</style>
