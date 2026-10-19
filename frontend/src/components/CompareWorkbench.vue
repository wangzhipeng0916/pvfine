<script setup lang="ts">
/**
 * PVF 归档差异对比与合并工作台（嵌入式 Tab 页签）
 * 集成作者原版差异对比与 IDEA 风格双栏实时合并：
 * - 嵌入主编辑区 Tab 容器，自适应 100% 铺满；
 * - 顶部配置栏支持折叠/展开，释放最大代码垂直浏览高度；
 * - 树形与平铺视图切换检索，树形视图目录严格禁止选中高亮与勾选；
 * - 文件多选与批量合并；
 * - 双栏实时编辑、段落合入（«）、Ctrl+S 保存、差异快速跳转；
 * - 完备的草稿切换保存保护与主编辑器数据强制刷新。
 *
 * @author flobit
 */
import { computed, h, onBeforeUnmount, onMounted, ref, watch, type VNode } from "vue";
import {
  ArrowClockwise20Regular,
  ArrowCollapseAll20Regular,
  ArrowDownload20Regular,
  ChevronDown20Regular,
  ChevronLeft20Regular,
  ChevronRight20Regular,
  ChevronUp20Regular,
  Document20Regular,
  DocumentArrowLeft20Regular,
  Folder20Regular,
  FolderOpen20Regular,
} from "@vicons/fluent";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDropdown,
  NEmpty,
  NIcon,
  NInput,
  NModal,
  NProgress,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSpin,
  NTooltip,
  NTree,
  NVirtualList,
  useMessage,
  type TreeOption,
} from "naive-ui";
import { useArchiveDiffStore, type DiffTreeNode } from "../stores/archiveDiff";
import { useArchiveStore } from "../stores/archive";
import { useSettingsStore } from "../stores/settings";
import { useExportStore } from "../stores/export";
import { archiveName, diffOperation, isArchiveDiffShortcutActive } from "../archiveDiff";
import { computeDiffLines } from "../textDiff";
import type { ResolvedThemeId } from "../theme";
import UnifiedDiff from "./UnifiedDiff.vue";
import SideBySideDiff from "./SideBySideDiff.vue";

const props = defineProps<{
  themeId?: ResolvedThemeId;
  active: boolean;
}>();

const diff = useArchiveDiffStore();
const archive = useArchiveStore();
const settings = useSettingsStore();
const exporter = useExportStore();
const message = useMessage();
const workbenchRef = ref<HTMLDivElement | null>(null);

function handleWorkbenchKeydown(e: KeyboardEvent): void {
  if (e.defaultPrevented || !isArchiveDiffShortcutActive(props.active, document.activeElement, workbenchRef.value)) return;
  const isAlt = e.altKey;
  const isMod = e.ctrlKey || e.metaKey;
  if (!isAlt || isMod) return;

  const code = e.code;
  // IDEA 规范：Alt + Left / Alt + Right 专用于差异文件之间的线性切换
  if (code === "ArrowLeft") {
    e.preventDefault();
    e.stopPropagation();
    void diff.openPreviousFile();
    return;
  }
  if (code === "ArrowRight") {
    e.preventDefault();
    e.stopPropagation();
    void diff.openNextFile();
    return;
  }
}

onMounted(() => {
  diff.open();
  window.addEventListener("keydown", handleWorkbenchKeydown, true);
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", handleWorkbenchKeydown, true);
});

const handledClickEvents = new WeakSet<MouseEvent>();
const directoryToggleKeys = new Set<string>();

// 顶部归档配置栏折叠状态：默认保持展开，绝不自动收起
const isHeaderCollapsed = ref(false);

watch(
  () => diff.status?.state,
  (state) => {
    // 当比对重置、中断或非就绪状态时，确保配置栏保持展开可见
    if (state !== "ready") {
      isHeaderCollapsed.value = false;
    }
  }
);

const oldPath = computed(() => (diff.reverse ? diff.status?.currentPath : diff.status?.otherPath));
const newPath = computed(() => (diff.reverse ? diff.status?.otherPath : diff.status?.currentPath));
const oldArchiveName = computed(() => archiveName(oldPath.value || ""));
const newArchiveName = computed(() => archiveName(newPath.value || ""));

const operationLabels: Record<string, string> = { add: "新增", modify: "变更", delete: "删除" };
const operationMarks: Record<string, string> = { add: "A", modify: "M", delete: "D" };

const parsedFile = computed(() => diff.displayedFile);
const parsedLines = computed(() => computeDiffLines(parsedFile.value));

const progress = computed(() =>
  diff.status?.total
    ? Math.min(diff.busy ? 99 : 100, Math.floor((diff.status.processed / diff.status.total) * 100))
    : 0
);

const progressLabel = computed(() => {
  const state = diff.rootLoading ? "building" : diff.status?.state;
  const labels: Record<string, string> = {
    loading: "正在加载归档",
    comparing: "正在比较文件",
    classifying: "正在整理差异",
    verifying: "正在复核文本内容",
    sorting: "正在排序差异",
    building: "正在生成差异树",
    "sorting-tree": "正在排序差异树",
  };
  const label = labels[state ?? "loading"] ?? "正在处理";
  return diff.status?.total && !diff.rootLoading
    ? `${label} ${diff.status.processed.toLocaleString()} / ${diff.status.total.toLocaleString()}`
    : `${label}…`;
});

const filterOptions = [
  { label: "全部差异", value: "all" },
  { label: "仅变更 (M)", value: "modify" },
  { label: "仅新增 (A)", value: "add" },
  { label: "仅删除 (D)", value: "delete" },
];

async function runExport(scopes: string[], title: string) {
  const status = diff.status;
  if (!status || status.state !== "ready" || diff.busy) return;
  try {
    const path = await exporter.open(
      { source: "archive-diff", scopes, commitId: "", diffId: status.id, reverse: diff.reverse },
      title
    );
    if (path) message.success(`已导出差异到 ${path}`);
  } catch (error: any) {
    message.error(`导出差异失败: ${error?.message ?? error}`);
  }
}

async function exportAllDiff() {
  const title = `导出全部差异（${oldArchiveName.value} → ${newArchiveName.value}）`;
  await runExport([], title);
}

async function exportSelectedDiff() {
  const count = diff.selectedPaths.size;
  if (count === 0) return;
  const title = `导出选中的差异（${count} 个文件/目录）`;
  await runExport(Array.from(diff.selectedPaths), title);
}

const exportDropdownOptions = computed(() => [
  { label: `导出选中的差异 (${diff.selectedPaths.size})`, key: "selected" },
  { label: `导出全部差异 (${diff.totalCount})`, key: "all" },
]);

function handleExportDropdownSelect(key: string) {
  if (key === "selected") {
    void exportSelectedDiff();
  } else {
    void exportAllDiff();
  }
}

async function handleMergeSelected() {
  const count = diff.selectedPaths.size;
  if (count === 0) return;
  const res = await diff.mergeSelected();
  if (res) {
    if (res.successCount > 0) message.success(`已成功合并 ${res.successCount} 个文件到本地工作区`);
    if (res.failedCount > 0) message.error(`${res.failedCount} 个文件合并失败: ${res.errors?.join("; ") || ""}`);
  }
}

// 树节点渲染：文件与目录均支持勾选（O(1) 集合判断），目录仅用于展开/折叠，绝不高亮选中
function renderLabel({ option }: { option: TreeOption }) {
  const treeNode = option as DiffTreeNode;
  const node = treeNode.node;
  const op = diffOperation(node.operation, diff.reverse);
  const dirState = node.isDir ? diff.getDirSelectionState(node.path, node.count, treeNode) : null;
  const checked = dirState ? dirState.checked : diff.isSelected(node.path);
  const indeterminate = dirState ? dirState.indeterminate : false;

  const checkboxVNode = h(NCheckbox, {
    checked,
    indeterminate,
    size: "small",
    style: "margin-right: 4px;",
    onClick: (e: MouseEvent) => e.stopPropagation(),
    "onUpdate:checked": () => {
      if (node.isDir) {
        void diff.toggleSelectDir(node.path, node.count, treeNode);
      } else {
        diff.toggleSelect(node.path);
      }
    },
  });

  return h("span", { class: ["tree-label", node.isDir ? "tree-label--dir" : `operation-${op}`], title: node.path }, [
    checkboxVNode,
    h("span", { class: "tree-mark" }, node.isDir ? h(Folder20Regular, { width: 14, height: 14 }) : operationMarks[op]),
    h("span", { class: "tree-name" }, node.name),
    node.isDir ? h("span", { class: "tree-count" }, String(node.count)) : null,
  ]);
}

async function openNode(option: TreeOption) {
  const node = (option as DiffTreeNode).node;
  if (!node.isDir) {
    await diff.openFile(node);
    return;
  }
  // 目录节点：只处理展开/折叠，绝对不选中高亮，也不打开文件
  const key = String(option.key);
  if (directoryToggleKeys.has(key)) return;
  directoryToggleKeys.add(key);
  try {
    const id = diff.status?.id;
    await diff.loadChildren(option);
    if (!id || diff.status?.id !== id) return;
    diff.expandedKeys = diff.expandedKeys.includes(key)
      ? diff.expandedKeys.filter((item) => item !== key)
      : [...diff.expandedKeys, key];
  } finally {
    directoryToggleKeys.delete(key);
  }
}

function isTreeControl(event: MouseEvent) {
  return event.target instanceof Element && !!event.target.closest("[data-switcher], [data-checkbox], .n-checkbox");
}

function nodeProps({ option }: { option: TreeOption }) {
  const node = (option as DiffTreeNode).node;
  return {
    onClick: (event: MouseEvent) => {
      if (handledClickEvents.has(event)) return;
      handledClickEvents.add(event);
      if (isTreeControl(event)) return;

      // 如果是目录节点：阻止选择高亮，仅展开/折叠
      if (node.isDir) {
        event.preventDefault();
        event.stopPropagation();
        void openNode(option);
        return;
      }

      if (settings.explorerOpenMode !== "single-click") return;
      event.stopPropagation();
      void openNode(option);
    },
    onDblclick: (event: MouseEvent) => {
      if (isTreeControl(event)) return;
      if (node.isDir) {
        event.preventDefault();
        event.stopPropagation();
        void openNode(option);
        return;
      }
      if (settings.explorerOpenMode !== "double-click") return;
      event.stopPropagation();
      void openNode(option);
    },
    onKeydown: (event: KeyboardEvent) => {
      if (event.key === "Enter") {
        event.preventDefault();
        if (node.isDir) {
          void openNode(option);
        } else {
          void diff.openFile(node);
        }
      }
    },
  };
}

function handleSelectedKeysUpdate(_keys: Array<string | number>, options: Array<TreeOption | null>) {
  diff.selectedKeys = options
    .filter((opt): opt is TreeOption => !!opt && !(opt as DiffTreeNode).node.isDir && opt.key !== undefined)
    .map((opt) => opt.key as string | number);
}

// 平铺列表展示与选中的差异文件集合直接复用 store 维护的有序可导航列表，消除重复过滤逻辑
const filteredFlatFiles = computed(() => diff.navigableFiles);

const isAllFlatSelected = computed(() => {
  if (filteredFlatFiles.value.length === 0) return false;
  return filteredFlatFiles.value.every((node) => diff.isSelected(node.path));
});

const isFlatIndeterminate = computed(() => {
  if (isAllFlatSelected.value) return false;
  return filteredFlatFiles.value.some((node) => diff.isSelected(node.path));
});

function toggleSelectAllFlat() {
  const paths = filteredFlatFiles.value.map((n) => n.path);
  if (isAllFlatSelected.value || isFlatIndeterminate.value) {
    diff.deselectAll(paths);
  } else {
    diff.selectAll(paths);
  }
}

let searchDebounceTimer: ReturnType<typeof setTimeout> | undefined;

function handleClearSearch() {
  clearTimeout(searchDebounceTimer);
  diff.clearSearch();
}

function handleSearchChange() {
  clearTimeout(searchDebounceTimer);
  const q = diff.searchQuery.trim();
  if (!q) {
    handleClearSearch();
    return;
  }
  searchDebounceTimer = setTimeout(() => {
    if (!diff.status?.id) return;
    if (diff.listMode === "flat") {
      void diff.loadFlatFiles();
    } else {
      void diff.searchTree(diff.searchQuery);
    }
  }, 250);
}

function switchToListMode(mode: "tree" | "flat") {
  diff.listMode = mode;
  if (!diff.status?.id) return;
  if (mode === "flat") {
    void diff.loadFlatFiles();
  } else if (diff.searchQuery.trim()) {
    void diff.searchTree(diff.searchQuery);
  }
}
</script>

<template>
  <div ref="workbenchRef" class="compare-workbench-root">
    <!-- 顶部配置栏 (支持折叠收起) -->
    <div v-show="!isHeaderCollapsed || diff.status?.state !== 'ready'" class="compare-header-bar">
      <!-- 本地归档展示 -->
      <div class="header-archive-side header-archive-side--local" :title="archive.info?.path || '当前工作区'">
        <span class="role-pill role-pill--local">本地当前</span>
        <NIcon size="15" class="pill-icon"><Document20Regular /></NIcon>
        <span class="pill-filename">{{ archiveName(archive.info?.path || '') }}</span>
        <span class="pill-path-subtle">{{ archive.info?.path || '当前已打开的归档' }}</span>
      </div>

      <!-- 对比连接标识与操作 -->
      <div
        class="header-vs-connector"
        :title="diff.reverse ? '当前对比视角: 本地当前 → 对比目标（以本地为基准观察外部）' : '当前对比视角: 对比目标 → 本地当前（以目标为基准观察本地）'"
      >
        <span class="vs-label">{{ diff.reverse ? '◀ VS' : 'VS ▶' }}</span>
      </div>

      <!-- 对比目标展示与操作 -->
      <div class="header-archive-side header-archive-side--target">
        <span class="role-pill role-pill--target">对比目标</span>
        <div class="target-input-box">
          <NInput
            :value="diff.otherPath"
            placeholder="请选择对比的目标 .pvf 归档文件..."
            size="small"
            readonly
            clearable
            @click="diff.selectArchive"
          >
            <template #prefix>
              <NIcon size="14"><FolderOpen20Regular /></NIcon>
            </template>
          </NInput>
        </div>

        <NButton
          size="small"
          :disabled="diff.busy || diff.selecting"
          :loading="diff.selecting"
          @click="diff.selectArchive"
        >
          <template #icon><NIcon><FolderOpen20Regular /></NIcon></template>
          {{ diff.otherPath ? '更换' : '浏览' }}
        </NButton>

        <NTooltip v-if="diff.otherPath" trigger="hover">
          <template #trigger>
            <NButton
              size="small"
              :disabled="!diff.status || diff.status.state !== 'ready'"
              @click="diff.toggleReverse"
            >
              {{ diff.reverse ? '反向视角 (本地→目标)' : '正向视角 (目标→本地)' }}
            </NButton>
          </template>
          {{ diff.reverse ? '当前以本地当前为基准，观察外部目标变更；点击切换' : '当前以对比目标为基准，观察本地当前变更；点击切换' }}
        </NTooltip>

        <NButton
          v-if="diff.status"
          size="small"
          :disabled="!diff.otherPath || diff.busy || diff.selecting"
          @click="diff.refresh"
        >
          <template #icon><NIcon><ArrowClockwise20Regular /></NIcon></template>
          刷新比较
        </NButton>

        <NButton v-if="diff.busy" size="small" :disabled="diff.starting" @click="diff.cancel">
          取消
        </NButton>

        <!-- 折叠按钮 -->
        <NTooltip v-if="diff.status?.state === 'ready'" trigger="hover">
          <template #trigger>
            <button class="collapse-header-btn" title="收起配置栏" @click="isHeaderCollapsed = true">
              <NIcon size="14"><ChevronUp20Regular /></NIcon>
              <span>收起</span>
            </button>
          </template>
          收起顶部配置栏，释放代码可视区域
        </NTooltip>
      </div>
    </div>

    <!-- 当配置栏收起时呈现的极简状态条 (高 28px) -->
    <div v-if="diff.status?.state === 'ready' && isHeaderCollapsed" class="compare-mini-summary-bar">
      <div class="mini-bar-left">
        <span class="mini-role-pill mini-role-pill--local">基准</span>
        <span class="mini-filename">{{ oldArchiveName }}</span>
        <span class="mini-vs">→</span>
        <span class="mini-role-pill mini-role-pill--target">目标</span>
        <span class="mini-filename">{{ newArchiveName }}</span>
      </div>
      <button class="mini-expand-btn" @click="isHeaderCollapsed = false">
        <NIcon size="13"><ChevronDown20Regular /></NIcon>
        <span>更换对比目标 / 展开配置</span>
      </button>
    </div>

    <!-- 错误警告 -->
    <NAlert v-if="diff.error" type="error" closable class="compare-error-alert">{{ diff.error }}</NAlert>

    <!-- 进度指示 -->
    <div v-if="diff.busy" class="compare-progress-box">
      <span>{{ progressLabel }}</span>
      <NProgress
        v-if="diff.status?.total && !diff.rootLoading"
        type="line"
        :percentage="progress"
        :show-indicator="false"
      />
    </div>

    <!-- 统计过滤栏与批量合入控制栏 -->
    <div v-if="diff.status" class="compare-toolbar-row">
      <div class="compare-filter-chips">
        <button
          type="button"
          class="filter-chip filter-chip--all"
          :class="{ 'is-active': diff.filterOperation === 'all' }"
          title="显示所有差异文件"
          @click="diff.setFilterOperation('all')"
        >
          <span class="chip-name">全部</span>
          <span class="chip-badge">{{ diff.totalCount }}</span>
        </button>

        <button
          type="button"
          class="filter-chip filter-chip--add"
          :class="{ 'is-active': diff.filterOperation === 'add' }"
          title="仅显示新增文件 (A)"
          @click="diff.setFilterOperation('add')"
        >
          <span class="chip-name">A 新增</span>
          <span class="chip-badge">{{ diff.counts.added }}</span>
        </button>

        <button
          type="button"
          class="filter-chip filter-chip--modify"
          :class="{ 'is-active': diff.filterOperation === 'modify' }"
          title="仅显示变更文件 (M)"
          @click="diff.setFilterOperation('modify')"
        >
          <span class="chip-name">M 变更</span>
          <span class="chip-badge">{{ diff.counts.modified }}</span>
        </button>

        <button
          type="button"
          class="filter-chip filter-chip--delete"
          :class="{ 'is-active': diff.filterOperation === 'delete' }"
          title="仅显示删除文件 (D)"
          @click="diff.setFilterOperation('delete')"
        >
          <span class="chip-name">D 删除</span>
          <span class="chip-badge">{{ diff.counts.deleted }}</span>
        </button>
      </div>

      <!-- 批量合入按钮 -->
      <NButton
        type="primary"
        size="small"
        :disabled="diff.busy || diff.merging || diff.selectedPaths.size === 0"
        :loading="diff.merging"
        @click="handleMergeSelected"
      >
        <template #icon>
          <DocumentArrowLeft20Regular />
        </template>
        合入选中文件 ({{ diff.selectedPaths.size }})
      </NButton>

      <!-- 统一导出差异控制（未勾选时导出全部，勾选后支持下拉选择导出选中/全部） -->
      <template v-if="diff.status?.state === 'ready' && (diff.tree.length || diff.flatFiles.length)">
        <NDropdown
          v-if="diff.selectedPaths.size > 0"
          trigger="click"
          :options="exportDropdownOptions"
          @select="handleExportDropdownSelect"
        >
          <NButton
            size="small"
            :disabled="diff.busy || exporter.visible"
          >
            <template #icon><NIcon><ArrowDownload20Regular /></NIcon></template>
            导出差异 ({{ diff.selectedPaths.size }})
            <NIcon size="12" style="margin-left: 2px"><ChevronDown20Regular /></NIcon>
          </NButton>
        </NDropdown>
        <NButton
          v-else
          size="small"
          :disabled="diff.busy || exporter.visible"
          @click="exportAllDiff"
        >
          <template #icon><NIcon><ArrowDownload20Regular /></NIcon></template>
          导出全部差异
        </NButton>
      </template>

      <!-- 视图模式切换 -->
      <div class="view-mode-toggle">
        <NRadioGroup v-model:value="diff.viewMode" size="small">
          <NRadioButton value="side-by-side">双栏合并</NRadioButton>
          <NRadioButton value="unified">单栏差异</NRadioButton>
        </NRadioGroup>
      </div>
    </div>

    <!-- 主体区域：左侧导航 + 右侧编辑器 -->
    <div class="compare-main-body">
      <!-- 左侧文件导航 -->
      <aside class="compare-aside-tree">
        <div class="tree-filter-bar">
          <NInput
            v-model:value="diff.searchQuery"
            size="small"
            placeholder="搜索路径..."
            clearable
            @update:value="handleSearchChange"
            @clear="handleClearSearch"
          />
          <div class="tree-mode-group">
            <NTooltip v-if="diff.listMode === 'tree'" trigger="hover">
              <template #trigger>
                <NButton
                  size="tiny"
                  quaternary
                  class="collapse-all-btn"
                  title="收起所有目录"
                  @click="diff.collapseAll"
                >
                  <template #icon>
                    <NIcon :size="14">
                      <ArrowCollapseAll20Regular />
                    </NIcon>
                  </template>
                </NButton>
              </template>
              收起所有目录
            </NTooltip>

            <NButton
              size="tiny"
              :quaternary="diff.listMode !== 'tree'"
              :secondary="diff.listMode === 'tree'"
              @click="switchToListMode('tree')"
            >
              树形
            </NButton>
            <NButton
              size="tiny"
              :quaternary="diff.listMode !== 'flat'"
              :secondary="diff.listMode === 'flat'"
              @click="switchToListMode('flat')"
            >
              平铺
            </NButton>
          </div>
        </div>

        <!-- 树形模式 (目录不可被选中) -->
        <template v-if="diff.listMode === 'tree'">
          <NTree
            v-if="diff.displayedTree.length"
            :selected-keys="diff.selectedKeys"
            v-model:expanded-keys="diff.expandedKeys"
            :data="diff.displayedTree"
            :on-load="diff.searchQuery.trim() ? undefined : diff.loadChildren"
            :render-label="renderLabel"
            :node-props="nodeProps"
            :selectable="true"
            virtual-scroll
            block-line
            :scrollbar-props="{ xScrollable: true }"
            :expand-on-click="false"
            class="difference-tree"
            @update:selected-keys="handleSelectedKeysUpdate"
          />
          <NEmpty
            v-else
            :description="
              diff.searchQuery.trim()
                ? '无匹配路径的文件'
                : diff.busy
                ? progressLabel
                : diff.status?.state === 'ready'
                ? '两份归档内容完全一致'
                : diff.status?.state === 'cancelled'
                ? '比较已取消，点击刷新重试'
                : '请在上方选择对比目标归档'
            "
          />
        </template>

        <!-- 平铺模式 -->
        <template v-else>
          <div class="flat-header">
            <NCheckbox
              :checked="isAllFlatSelected"
              :indeterminate="isFlatIndeterminate"
              size="small"
              @update:checked="toggleSelectAllFlat"
            >
              全选 ({{ filteredFlatFiles.length }})
            </NCheckbox>
          </div>
          <div v-if="diff.flatLoading" class="flat-loading">
            <NSpin size="small" />
          </div>
          <div v-else-if="filteredFlatFiles.length" class="flat-file-list">
            <NVirtualList
              style="height: 100%; max-height: 100%;"
              :items="filteredFlatFiles"
              :item-size="28"
              key-field="path"
            >
              <template #default="{ item }">
                <div
                  :class="[
                    'flat-file-row',
                    `operation-${diffOperation(item.operation, diff.reverse)}`,
                    diff.activeNode?.path === item.path ? 'is-active' : ''
                  ]"
                  @click="diff.openFile(item)"
                >
                  <NCheckbox
                    :checked="diff.isSelected(item.path)"
                    size="small"
                    @click.stop
                    @update:checked="() => diff.toggleSelect(item.path)"
                  />
                  <span class="flat-mark">{{ operationMarks[diffOperation(item.operation, diff.reverse)] }}</span>
                  <span class="flat-path" :title="item.path">{{ item.path }}</span>
                </div>
              </template>
            </NVirtualList>
          </div>
          <NEmpty v-else description="无匹配的差异文件" />
        </template>
      </aside>

      <!-- 右侧详情展示与双栏编辑合并 -->
      <main class="compare-editor-detail">
        <NSpin v-if="diff.fileLoading" class="detail-loading" />
        <NAlert v-else-if="diff.fileError" type="error">{{ diff.fileError }}</NAlert>
        <template v-else-if="parsedFile">
          <!-- 双栏 Live Merge 模式（工作台物理侧恒定锚定） -->
          <SideBySideDiff
            v-if="diff.viewMode === 'side-by-side' && diff.file?.textAvailable"
            :file="diff.file"
            :active="props.active"
          />

          <!-- 单栏 Unified 模式 -->
          <template v-else>
            <div class="file-heading">
              <span :class="`operation-${parsedFile.operation}`">
                {{ operationLabels[parsedFile.operation] }}
              </span>
              <strong :title="parsedFile.path">{{ parsedFile.path }}</strong>

              <!-- 单栏与非文本文件下的差异文件导航按钮 -->
              <div class="file-navigation-actions">
                <NTooltip trigger="hover">
                  <template #trigger>
                    <NButton
                      size="tiny"
                      quaternary
                      :disabled="!diff.hasPreviousFile"
                      aria-label="查看上一个差异文件 (Alt+Left)"
                      @click="diff.openPreviousFile()"
                    >
                      <template #icon>
                        <NIcon :size="14"><ChevronLeft20Regular /></NIcon>
                      </template>
                    </NButton>
                  </template>
                  查看上一个差异文件 (Alt+Left){{ diff.fileNavIndicator ? ` [${diff.fileNavIndicator}]` : '' }}
                </NTooltip>
                <span class="file-nav-indicator">{{ diff.fileNavIndicator }}</span>
                <NTooltip trigger="hover">
                  <template #trigger>
                    <NButton
                      size="tiny"
                      quaternary
                      :disabled="!diff.hasNextFile"
                      aria-label="查看下一个差异文件 (Alt+Right)"
                      @click="diff.openNextFile()"
                    >
                      <template #icon>
                        <NIcon :size="14"><ChevronRight20Regular /></NIcon>
                      </template>
                    </NButton>
                  </template>
                  查看下一个差异文件 (Alt+Right){{ diff.fileNavIndicator ? ` [${diff.fileNavIndicator}]` : '' }}
                </NTooltip>
              </div>
            </div>
            <div class="file-metadata">
              <div>
                <small>基准</small>
                <span v-if="parsedFile.beforeExists">
                  类型 {{ parsedFile.beforeType }}，{{ parsedFile.beforeSize.toLocaleString() }} 字节
                </span>
                <span v-else>不存在</span>
                <code v-if="parsedFile.beforeExists && parsedFile.beforeHash" :title="parsedFile.beforeHash">
                  {{ parsedFile.beforeHash }}
                </code>
              </div>
              <div>
                <small>目标</small>
                <span v-if="parsedFile.afterExists">
                  类型 {{ parsedFile.afterType }}，{{ parsedFile.afterSize.toLocaleString() }} 字节
                </span>
                <span v-else>不存在</span>
                <code v-if="parsedFile.afterExists && parsedFile.afterHash" :title="parsedFile.afterHash">
                  {{ parsedFile.afterHash }}
                </code>
              </div>
            </div>
            <UnifiedDiff v-if="parsedFile.textAvailable && parsedLines.lines.length" :diff="parsedFile" />
            <NEmpty
              v-else
              :description="
                parsedFile.message ||
                (!parsedFile.beforeText && !parsedFile.afterText
                  ? `${operationLabels[parsedFile.operation]}空文件，没有文本行级差异`
                  : '没有文本行级差异')
              "
              class="detail-empty"
            />
          </template>
        </template>
        <NEmpty v-else description="点击左侧文件查看差异与合并内容" class="detail-empty" />
      </main>
    </div>

    <!-- 草稿保存确认对话框 -->
    <NModal
      :show="diff.showDraftConfirmModal"
      preset="dialog"
      title="草稿未保存"
      content="当前文件存在未保存的本地修改。是否在切换前保存并同步到归档？"
      positive-text="保存并切换"
      negative-text="放弃修改"
      @positive-click="diff.confirmDraftSaveAndSwitch"
      @negative-click="diff.confirmDraftDiscardAndSwitch"
      @close="diff.cancelDraftSwitch"
    />
  </div>
</template>

<style scoped>
.compare-workbench-root {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--pvf-surface-card);
  color: var(--pvf-text-primary);
}

.compare-header-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 8px 16px;
  background: var(--pvf-surface-subtle);
  border-bottom: 1px solid var(--pvf-border-subtle);
  flex-wrap: wrap;
}

.header-archive-side {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.header-archive-side--local {
  flex: 0 1 auto;
}

.header-archive-side--target {
  flex: 1 1 auto;
}

.role-pill {
  font-size: 11px;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: 4px;
  flex-shrink: 0;
}

.role-pill--local {
  background: rgba(46, 160, 67, 0.15);
  color: var(--pvf-success-hover);
  border: 1px solid rgba(46, 160, 67, 0.3);
}

.role-pill--target {
  background: rgba(31, 111, 235, 0.15);
  color: #58a6ff;
  border: 1px solid rgba(31, 111, 235, 0.3);
}

.pill-filename {
  font-weight: 600;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pill-path-subtle {
  font-size: 11px;
  color: var(--pvf-text-muted);
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.header-vs-connector {
  font-weight: 700;
  color: var(--pvf-text-muted);
  font-size: 12px;
  padding: 0 4px;
}

.target-input-box {
  width: 260px;
}

.collapse-header-btn {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: transparent;
  border: none;
  color: var(--pvf-text-muted);
  font-size: 11px;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 4px;
}

.collapse-header-btn:hover {
  background: var(--pvf-surface-hover);
  color: var(--pvf-text-primary);
}

.compare-mini-summary-bar {
  height: 28px;
  min-height: 28px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
  background: var(--pvf-surface-subtle);
  border-bottom: 1px solid var(--pvf-border-subtle);
  font-size: 11px;
}

.mini-bar-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.mini-role-pill {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 4px;
  border-radius: 3px;
}

.mini-role-pill--local {
  background: rgba(46, 160, 67, 0.15);
  color: var(--pvf-success-hover);
}

.mini-role-pill--target {
  background: rgba(31, 111, 235, 0.15);
  color: #58a6ff;
}

.mini-filename {
  font-weight: 500;
  color: var(--pvf-text-secondary);
}

.mini-vs {
  color: var(--pvf-text-muted);
  font-weight: bold;
}

.mini-expand-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: transparent;
  border: none;
  color: var(--pvf-text-muted);
  font-size: 11px;
  cursor: pointer;
}

.mini-expand-btn:hover {
  color: var(--pvf-text-primary);
}

.compare-error-alert {
  margin: 6px 16px;
}

.compare-progress-box {
  display: grid;
  gap: 4px;
  padding: 6px 16px;
  background: var(--pvf-surface-subtle);
  font-size: 12px;
}

.compare-toolbar-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 16px;
  border-bottom: 1px solid var(--pvf-border-subtle);
  background: var(--pvf-surface-subtle);
  flex-wrap: wrap;
}

.compare-filter-chips {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  white-space: nowrap;
  flex-shrink: 0;
}

.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  border: 1px solid var(--pvf-border-subtle);
  background: var(--pvf-surface-card);
  color: var(--pvf-text-secondary);
  cursor: pointer;
  user-select: none;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.filter-chip:hover {
  background: var(--pvf-surface-hover);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-focus);
}

.filter-chip .chip-name {
  font-weight: 500;
}

.filter-chip .chip-badge {
  font-size: 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  padding: 0 4px;
  border-radius: 3px;
  background: rgba(255, 255, 255, 0.08);
}

.filter-chip.is-active {
  font-weight: 600;
  border-color: currentColor;
}

.filter-chip--all.is-active {
  background: rgba(255, 255, 255, 0.12);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-focus);
}

.filter-chip--add {
  color: var(--pvf-success-hover, #3fb950);
}
.filter-chip--add.is-active {
  background: rgba(46, 160, 67, 0.18);
  border-color: rgba(46, 160, 67, 0.5);
}

.filter-chip--modify {
  color: var(--pvf-warning-hover, #d29922);
}
.filter-chip--modify.is-active {
  background: rgba(210, 153, 34, 0.18);
  border-color: rgba(210, 153, 34, 0.5);
}

.filter-chip--delete {
  color: var(--pvf-error-hover, #f85149);
}
.filter-chip--delete.is-active {
  background: rgba(248, 81, 73, 0.18);
  border-color: rgba(248, 81, 73, 0.5);
}

.operation-add, :deep(.operation-add) {
  color: var(--pvf-success-hover);
}

.operation-modify, :deep(.operation-modify) {
  color: var(--pvf-warning-hover);
}

.operation-delete, :deep(.operation-delete) {
  color: var(--pvf-error-hover);
}

.compare-main-body {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(260px, 28%) minmax(0, 1fr);
}

.compare-aside-tree {
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 8px;
  border-right: 1px solid var(--pvf-border-subtle);
  background: var(--pvf-surface-panel);
}

.tree-filter-bar {
  display: flex;
  gap: 6px;
  margin-bottom: 8px;
}

.tree-mode-group {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}

.collapse-all-btn {
  padding: 0 4px;
  color: var(--pvf-text-muted);
}

.collapse-all-btn:hover {
  color: var(--pvf-text-primary);
}

.difference-tree {
  flex: 1;
  min-height: 0;
  height: 100%;
  width: 100%;
}

:deep(.n-tree-node) {
  width: max-content;
  min-width: 100%;
}

:deep(.n-tree-node-content) {
  width: max-content;
  min-width: 100%;
}

:deep(.n-tree-node-content__text) {
  flex: 0 0 auto;
  width: max-content;
  min-width: max-content;
  max-width: none;
  overflow: visible;
}

:deep(.tree-label) {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: max-content;
  width: max-content;
  max-width: none;
  overflow: visible;
  white-space: nowrap;
  vertical-align: middle;
}

:deep(.tree-label--dir) {
  cursor: pointer;
  user-select: none;
}

:deep(.tree-mark) {
  width: 14px;
  font-family: monospace;
  flex-shrink: 0;
}

:deep(.tree-name) {
  flex-shrink: 0;
  min-width: max-content;
  overflow: visible;
  white-space: nowrap;
}

:deep(.tree-count) {
  margin-left: auto;
  padding-left: 6px;
  color: var(--pvf-text-muted);
  font-size: 11px;
}

.flat-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 8px 6px;
  border-bottom: 1px solid var(--pvf-border-subtle);
}

.flat-file-list {
  flex: 1;
  min-height: 0;
  height: 100%;
  overflow-x: auto;
  overflow-y: hidden;
}

.flat-file-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 6px;
  height: 28px;
  box-sizing: border-box;
  border-radius: 4px;
  cursor: pointer;
  font-size: 12px;
  width: max-content;
  min-width: 100%;
}

.flat-file-row:hover {
  background: var(--pvf-surface-hover);
}

.flat-file-row.is-active {
  background: var(--pvf-surface-selected);
}

.flat-mark {
  width: 14px;
  font-family: monospace;
  font-weight: bold;
  flex-shrink: 0;
}

.flat-path {
  flex: 0 0 auto;
  min-width: max-content;
  overflow: visible;
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.flat-loading {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 24px;
}

.compare-editor-detail {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.file-heading {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--pvf-border-subtle);
}

.file-heading strong {
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.file-navigation-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
  flex-shrink: 0;
}

.file-nav-indicator {
  font-size: 11px;
  color: var(--pvf-text-secondary);
  padding: 0 4px;
  font-variant-numeric: tabular-nums;
  font-weight: 500;
}

.file-metadata {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  padding: 10px 12px;
  color: var(--pvf-text-secondary);
  font-size: 12px;
}

.file-metadata > div {
  display: grid;
  gap: 3px;
  min-width: 0;
}

.file-metadata small {
  color: var(--pvf-text-muted);
}

.file-metadata code {
  font-size: 10px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-empty,
.detail-loading {
  margin: auto;
  padding: 24px;
}

@media (max-width: 768px) {
  .compare-main-body {
    grid-template-columns: 220px minmax(0, 1fr);
  }
}
</style>
