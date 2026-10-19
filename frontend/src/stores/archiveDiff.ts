import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { Events } from "@wailsio/runtime";
import type { TreeOption } from "naive-ui";
import { ArchiveDiffService } from "../../bindings/pvfine/services";
import type { ArchiveDiffStatus, ArchiveDiffFile, ArchiveDiffNode } from "../../bindings/pvfine/services/models";
import { useEditorStore } from "./editor";
import { diffOperation, orientArchiveDiff } from "../archiveDiff";

export type DiffTreeNode = TreeOption & { node: ArchiveDiffNode; children?: DiffTreeNode[] };
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
function treeNodes(nodes: ArchiveDiffNode[]): DiffTreeNode[] {
  return nodes.map(node => ({
    key: node.path.toLowerCase(),
    label: node.name,
    isLeaf: !node.isDir,
    node,
    selectable: !node.isDir,
  }));
}

/**
 * 递归收集节点（包括目录及其所有子孙目录）下所有的叶子文件路径
 * @author flobit
 */
export function collectLeafPaths(node: DiffTreeNode): string[] {
  const leaves: string[] = [];
  function dfs(curr: DiffTreeNode) {
    if (!curr.node.isDir) {
      if (curr.node.path) {
        leaves.push(curr.node.path);
      }
      return;
    }
    if (curr.children && curr.children.length > 0) {
      for (const child of curr.children) {
        dfs(child as DiffTreeNode);
      }
    }
  }
  dfs(node);
  return leaves;
}

/**
 * 根据路径深度优先查找目录或文件节点
 * @author flobit
 */
export function findNodeByPath(nodes: DiffTreeNode[], targetPath: string): DiffTreeNode | null {
  const target = targetPath.toLowerCase().replace(/\/+$/, "");
  for (const node of nodes) {
    const current = (node.node?.path || "").toLowerCase().replace(/\/+$/, "");
    if (current === target) return node;
    if (node.children && node.children.length > 0) {
      const found = findNodeByPath(node.children as DiffTreeNode[], targetPath);
      if (found) return found;
    }
  }
  return null;
}

/**
 * 保留已加载的层级，但重新读取内容，避免保留已消失或被筛选掉的差异。
 */
export async function preserveTreeExpansion(
  newNodes: DiffTreeNode[],
  oldNodes: DiffTreeNode[],
  load: (path: string) => Promise<DiffTreeNode[]>,
): Promise<void> {
  await Promise.all(newNodes.map(async newNode => {
    const oldNode = findNodeByPath(oldNodes, newNode.node.path);
    if (newNode.node.isDir && oldNode?.children) {
      newNode.children = await load(newNode.node.path);
      await preserveTreeExpansion(newNode.children, oldNode.children, load);
    }
  }));
}

/**
 * 递归收集树节点列表中所有符合条件的叶子文件节点，按树的自然深度优先序排列
 * @author flobit
 */
export function collectLeavesFromTree(roots: DiffTreeNode[], op = "all", reverse = false, query = ""): ArchiveDiffNode[] {
  const leaves: ArchiveDiffNode[] = [];
  const q = query.trim().toLowerCase();
  function walk(curr: DiffTreeNode) {
    if (!curr.node.isDir) {
      const currentOp = diffOperation(curr.node.operation, reverse);
      if (op !== "all" && op && currentOp !== op) return;
      if (q && !curr.node.path.toLowerCase().includes(q)) return;
      leaves.push(curr.node);
      return;
    }
    if (curr.children && curr.children.length > 0) {
      for (const c of curr.children) walk(c as DiffTreeNode);
    }
  }
  for (const r of roots) walk(r);
  return leaves;
}

export const useArchiveDiffStore = defineStore("archiveDiff", () => {
  const visible = ref(false);
  const otherPath = ref("");
  const reverse = ref(false);
  const status = ref<ArchiveDiffStatus | null>(null);
  const tree = ref<DiffTreeNode[]>([]);
  const file = ref<ArchiveDiffFile | null>(null);
  const activeNode = ref<ArchiveDiffNode | null>(null);
  const selectedKeys = ref<Array<string | number>>([]);
  const expandedKeys = ref<Array<string | number>>([]);
  const starting = ref(false);
  const selecting = ref(false);
  const fileLoading = ref(false);
  const rootLoading = ref(false);
  const error = ref("");
  const fileError = ref("");
  let epoch = 0;
  let fileEpoch = 0;
  let viewEpoch = 0;
  let treeSeq = 0;
  let flatSeq = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pendingStart: Promise<ArchiveDiffStatus | null> | undefined;

  // 视图模式与列表检索模式
  const viewMode = ref<"side-by-side" | "unified">("side-by-side");
  const listMode = ref<"tree" | "flat">("tree");
  const filterOperation = ref<string>("all");
  const searchQuery = ref<string>("");
  const flatFiles = ref<ArchiveDiffNode[]>([]);
  const flatLoading = ref(false);
  const lineWrapping = ref(false);

  // 多选勾选集合与合并状态
  const selectedPaths = ref<Set<string>>(new Set());
  const merging = ref(false);

  // 双栏编辑器内草稿与切换保存保护
  const draftPath = ref("");
  const draftText = ref("");
  const originalDraftText = ref("");
  const isDraftDirty = computed(() => !!draftPath.value && draftText.value !== originalDraftText.value);
  const pendingSwitchNode = ref<ArchiveDiffNode | null>(null);
  const showDraftConfirmModal = ref(false);

  const busy = computed(() => starting.value || rootLoading.value || ["loading", "comparing", "classifying", "verifying", "sorting", "building", "sorting-tree"].includes(status.value?.state ?? ""));
  const displayedFile = computed(() => orientArchiveDiff(file.value, reverse.value));
  const counts = computed(() => ({
    added: (reverse.value ? status.value?.deleted : status.value?.added) ?? 0,
    modified: status.value?.modified ?? 0,
    deleted: (reverse.value ? status.value?.added : status.value?.deleted) ?? 0,
  }));
  const totalCount = computed(() => counts.value.added + counts.value.modified + counts.value.deleted);

  function getBackendOperation(op: string): string {
    if (op === "all" || !op) return "all";
    if (!reverse.value) return op;
    if (op === "add") return "delete";
    if (op === "delete") return "add";
    return op;
  }

  function setDraft(path: string, text: string, originalText: string) {
    draftPath.value = path;
    draftText.value = text;
    originalDraftText.value = originalText;
  }

  function updateDraftText(text: string) {
    draftText.value = text;
  }

  function discardDraft() {
    draftText.value = originalDraftText.value;
    draftPath.value = "";
    originalDraftText.value = "";
  }

  const dirSelectedCount = computed(() => {
    const counts = new Map<string, number>();
    for (const raw of selectedPaths.value) {
      let p = raw.toLowerCase();
      while (true) {
        const idx = p.lastIndexOf("/");
        if (idx <= 0) break;
        p = p.substring(0, idx);
        counts.set(p, (counts.get(p) ?? 0) + 1);
      }
    }
    return counts;
  });

  function isSelected(path: string): boolean {
    return selectedPaths.value.has(path.toLowerCase());
  }

  function getDirLeafPaths(path: string, targetNode?: DiffTreeNode): string[] {
    if (!searchQuery.value.trim()) return [];
    const node = targetNode ?? findNodeByPath(searchedTreeNodes.value, path);
    if (node && node.node.isDir && node.children && node.children.length > 0) {
      return collectLeafPaths(node);
    }
    return [];
  }

  function getDirSelectionState(path: string, count: number, targetNode?: DiffTreeNode): { checked: boolean; indeterminate: boolean } {
    if (count <= 0) return { checked: false, indeterminate: false };
    const leaves = getDirLeafPaths(path, targetNode);
    if (leaves.length > 0) {
      let selected = 0;
      for (const p of leaves) {
        if (selectedPaths.value.has(p.toLowerCase())) {
          selected++;
        }
      }
      return {
        checked: selected === leaves.length,
        indeterminate: selected > 0 && selected < leaves.length,
      };
    }
    const c = dirSelectedCount.value.get(path.toLowerCase()) ?? 0;
    return {
      checked: c >= count,
      indeterminate: c > 0 && c < count,
    };
  }

  function isDirSelected(path: string, count: number, targetNode?: DiffTreeNode): boolean {
    return getDirSelectionState(path, count, targetNode).checked;
  }

  function isDirIndeterminate(path: string, count: number, targetNode?: DiffTreeNode): boolean {
    return getDirSelectionState(path, count, targetNode).indeterminate;
  }

  function toggleSelect(path: string) {
    const lower = path.toLowerCase();
    const next = new Set(selectedPaths.value);
    if (next.has(lower)) {
      next.delete(lower);
    } else {
      next.add(lower);
    }
    selectedPaths.value = next;
  }

  async function getMatchingFilesUnderDir(dirPath: string, targetNode?: DiffTreeNode): Promise<string[]> {
    const q = searchQuery.value.trim().toLowerCase();
    // 1. 若处于关键字搜索过滤状态，优先从当前展示的过滤树节点收集所有可见叶子文件，避免异步竞态与隐藏文件泄漏
    if (q) {
      const leaves = getDirLeafPaths(dirPath, targetNode);
      if (leaves.length > 0) {
        return leaves;
      }
    }

    // 2. 否则通过后端接口拉取匹配文件，并在前端对关键字与目录路径进行双重过滤防线
    const id = status.value?.id;
    if (!id) return [];
    const token = epoch;
    const viewToken = viewEpoch;
    const op = getBackendOperation(filterOperation.value);
    const res = await ArchiveDiffService.FlatFiles(id, searchQuery.value, op);
    if (token !== epoch || viewToken !== viewEpoch) return [];
    const normalizedDir = dirPath ? dirPath.toLowerCase().replace(/\/+$/, "") + "/" : "";
    return (res ?? [])
      .filter((f) => {
        if (f.isDir) return false;
        const lowerPath = f.path.toLowerCase();
        if (normalizedDir && !lowerPath.startsWith(normalizedDir)) return false;
        if (q && !lowerPath.includes(q)) return false;
        return true;
      })
      .map((f) => f.path);
  }

  async function toggleSelectDir(path: string, _totalCount?: number, targetNode?: DiffTreeNode) {
    const matching = await getMatchingFilesUnderDir(path, targetNode);
    if (matching.length === 0) return;
    const next = new Set(selectedPaths.value);
    const allSelected = matching.every((p) => next.has(p.toLowerCase()));
    if (allSelected) {
      for (const p of matching) {
        next.delete(p.toLowerCase());
      }
    } else {
      for (const p of matching) {
        next.add(p.toLowerCase());
      }
    }
    selectedPaths.value = next;
  }

  function selectAll(paths: string[]) {
    const next = new Set(selectedPaths.value);
    for (const p of paths) next.add(p.toLowerCase());
    selectedPaths.value = next;
  }

  function deselectPaths(paths: string[]): void {
    const next = new Set(selectedPaths.value);
    for (const p of paths) {
      next.delete(p.toLowerCase());
    }
    selectedPaths.value = next;
  }

  function deselectAll(paths: string[]): void {
    deselectPaths(paths);
  }

  function clearSelection() {
    selectedPaths.value = new Set();
  }

  function buildTreeFromFiles(files: ArchiveDiffNode[]): { tree: DiffTreeNode[]; dirKeys: string[] } {
    const root: DiffTreeNode[] = [];
    const dirMap = new Map<string, DiffTreeNode>();
    const dirKeys: string[] = [];

    for (const file of files) {
      const parts = file.path.split("/");
      let currentPath = "";
      let parentChildren = root;

      for (let i = 0; i < parts.length - 1; i++) {
        const part = parts[i];
        currentPath = currentPath ? `${currentPath}/${part}` : part;
        const lowerPath = currentPath.toLowerCase();

        let dirNode = dirMap.get(lowerPath);
        if (!dirNode) {
          dirNode = {
            key: lowerPath,
            label: part,
            isLeaf: false,
            selectable: false,
            children: [],
            node: {
              name: part,
              path: currentPath,
              isDir: true,
              operation: "modify",
              count: 0,
            },
          };
          dirMap.set(lowerPath, dirNode);
          dirKeys.push(lowerPath);
          parentChildren.push(dirNode);
        }
        dirNode.node.count++;
        parentChildren = dirNode.children!;
      }

      const fileName = parts[parts.length - 1];
      parentChildren.push({
        key: file.path.toLowerCase(),
        label: fileName,
        isLeaf: true,
        selectable: true,
        node: file,
      });
    }
    return { tree: root, dirKeys };
  }

  const searchedTreeNodes = ref<DiffTreeNode[]>([]);
  const savedExpandedKeys = ref<Array<string | number> | null>(null);
  let searchTreeSeq = 0;

  async function searchTree(query: string, silent = false) {
    const q = query.trim();
    if (!q) {
      clearSearch();
      return;
    }
    const id = status.value?.id;
    if (!id) return;

    if (savedExpandedKeys.value === null) {
      savedExpandedKeys.value = [...expandedKeys.value];
    }

    const currentSeq = ++searchTreeSeq;
    const token = epoch;
    const viewToken = viewEpoch;
    try {
      const op = getBackendOperation(filterOperation.value);
      const res = await ArchiveDiffService.FlatFiles(id, q, op);
      if (currentSeq !== searchTreeSeq || token !== epoch || viewToken !== viewEpoch) return;
      const files = (res ?? []).filter((f) => !f.isDir);
      const limited = files.slice(0, 1000);
      const { tree: resultTree, dirKeys } = buildTreeFromFiles(limited);
      searchedTreeNodes.value = resultTree;
      if (!silent) {
        expandedKeys.value = dirKeys;
      } else {
        const mergedKeys = new Set([...expandedKeys.value, ...dirKeys]);
        expandedKeys.value = Array.from(mergedKeys);
      }
    } catch {
      if (currentSeq === searchTreeSeq && token === epoch && viewToken === viewEpoch) {
        searchedTreeNodes.value = [];
      }
    }
  }

  function clearSearch() {
    searchTreeSeq++;
    searchQuery.value = "";
    searchedTreeNodes.value = [];
    if (savedExpandedKeys.value !== null) {
      expandedKeys.value = savedExpandedKeys.value;
      savedExpandedKeys.value = null;
    }
    if (listMode.value === "flat") {
      void loadFlatFiles();
    }
  }

  function collapseAll() {
    expandedKeys.value = [];
    savedExpandedKeys.value = [];
  }

  const displayedTree = computed<TreeOption[]>(() => {
    if (searchQuery.value.trim()) {
      return searchedTreeNodes.value;
    }
    return tree.value;
  });

  function clearResult() {
    viewEpoch++;
    treeSeq++;
    flatSeq++;
    searchTreeSeq++;
    flatLoading.value = false;
    merging.value = false;
    rootLoading.value = false;
    clearTimeout(timer);
    status.value = null;
    tree.value = [];
    searchedTreeNodes.value = [];
    savedExpandedKeys.value = null;
    file.value = null;
    activeNode.value = null;
    selectedKeys.value = [];
    expandedKeys.value = [];
    selectedPaths.value = new Set();
    flatFiles.value = [];
    discardDraft();
    showDraftConfirmModal.value = false;
    pendingSwitchNode.value = null;
    fileEpoch++;
    fileLoading.value = false;
    fileError.value = "";
  }

  function open() { visible.value = true; }
  function close() {
    epoch++;
    visible.value = false;
    starting.value = false;
    selecting.value = false;
    clearResult();
    otherPath.value = "";
    reverse.value = false;
    filterOperation.value = "all";
    error.value = "";
    void ArchiveDiffService.Release().catch(() => {});
  }

  async function selectArchive() {
    visible.value = true;
    const token = epoch;
    selecting.value = true;
    try {
      const path = await ArchiveDiffService.SelectArchive();
      if (token !== epoch || !path) return;
      otherPath.value = path;
      selecting.value = false;
      await refresh();
    } catch (err) {
      if (token === epoch) error.value = errorMessage(err);
    } finally { if (token === epoch) selecting.value = false; }
  }

  async function refresh() {
    if (!otherPath.value || busy.value) return;
    let drafts;
    try { drafts = useEditorStore().archiveDiffDrafts(); }
    catch (err) { error.value = errorMessage(err); return; }
    const token = ++epoch;
    clearResult();
    starting.value = true;
    error.value = "";
    try {
      // Start replaces the backend session; serialize outstanding start calls.
      if (pendingStart) await pendingStart.catch(() => {});
      if (token !== epoch) return;
      const request = ArchiveDiffService.Start({ path: otherPath.value, drafts });
      pendingStart = request;
      const result = await request;
      if (token !== epoch) {
        if (result) await ArchiveDiffService.Cancel(result.id);
        return;
      }
      if (!result) throw new Error("未收到对比会话");
      status.value = result;
      starting.value = false;
      await poll(token, result.id);
    } catch (err) {
      if (token === epoch) { error.value = errorMessage(err); clearResult(); }
    } finally { if (token === epoch) starting.value = false; }
  }

  async function loadRootTree() {
    const id = status.value?.id;
    if (!id) return;
    const token = epoch;
    rootLoading.value = true;
    try {
      await refreshTreePreservingExpansion();
    } catch (err) {
      if (token === epoch) error.value = errorMessage(err);
    } finally {
      if (token === epoch) rootLoading.value = false;
    }
  }

  async function poll(token: number, id: string) {
    try {
      const result = await ArchiveDiffService.Status(id);
      if (token !== epoch) return;
      if (!result) throw new Error("对比会话已关闭");
      status.value = result;
      if (result.state === "ready") {
        await loadRootTree();
        if (token !== epoch) return;
        if (listMode.value === "flat") {
          void loadFlatFiles();
        } else if (searchQuery.value.trim()) {
          void searchTree(searchQuery.value);
        } else {
          void loadFlatFiles(true);
        }
      } else if (result.state === "error") {
        error.value = result.error;
      } else if (result.state !== "cancelled") {
        timer = setTimeout(() => void poll(token, id), 250);
      }
    } catch (err) { if (token === epoch) { error.value = errorMessage(err); clearResult(); } }
  }

  async function loadChildren(option: TreeOption) {
    const target = option as DiffTreeNode;
    const token = epoch;
    const viewToken = viewEpoch;
    const requestSeq = treeSeq;
    const id = status.value?.id;
    if (!id || target.children) return;
    try {
      const op = getBackendOperation(filterOperation.value);
      const nodes = await ArchiveDiffService.FilteredChildren(id, target.node.path, op);
      if (token !== epoch || viewToken !== viewEpoch || requestSeq !== treeSeq) return;
      target.children = treeNodes(nodes ?? []);
      tree.value = [...tree.value];
    } catch (err) { if (token === epoch) error.value = errorMessage(err); }
  }


  /**
   * 平滑拉取根节点并保留现有所有已展开子树，避免抖动。
   * @author flobit
   */
  async function refreshTreePreservingExpansion(): Promise<void> {
    const id = status.value?.id;
    if (!id) return;
    const token = epoch;
    const viewToken = viewEpoch;
    const seq = ++treeSeq;
    const op = getBackendOperation(filterOperation.value);
    try {
      const nodes = await ArchiveDiffService.FilteredChildren(id, "", op);
      if (token !== epoch || viewToken !== viewEpoch || seq !== treeSeq) return;
      const newRoots = treeNodes(nodes ?? []);
      await preserveTreeExpansion(newRoots, tree.value, async path => {
        if (token !== epoch || viewToken !== viewEpoch || seq !== treeSeq) return [];
        return treeNodes(await ArchiveDiffService.FilteredChildren(id, path, op) ?? []);
      });
      if (token !== epoch || viewToken !== viewEpoch || seq !== treeSeq) return;
      tree.value = newRoots;
    } catch (err) {
      if (token === epoch && viewToken === viewEpoch && seq === treeSeq) error.value = errorMessage(err);
    }
  }

  /**
   * 刷新当前差异视图（平铺列表或目录树）。
   * 仅重读已加载层级，保留展开状态，同时刷新所有祖先的统计。
   * @author flobit
   */
  async function refreshCurrentView(_savedPath?: string): Promise<void> {
    await Promise.all([
      refreshTreePreservingExpansion(),
      loadFlatFiles(true),
      listMode.value === "tree" && searchQuery.value.trim()
        ? searchTree(searchQuery.value, true) : Promise.resolve(),
    ]);
  }

  async function setFilterOperation(op: string) {
    if (filterOperation.value === op) return;
    filterOperation.value = op;
    viewEpoch++;
    expandedKeys.value = [];
    await refreshCurrentView();
  }

  async function toggleReverse() {
    reverse.value = !reverse.value;
    viewEpoch++;
    expandedKeys.value = [];
    await refreshCurrentView();
  }

  async function loadFlatFiles(silent = false) {
    const id = status.value?.id;
    if (!id) {
      flatFiles.value = [];
      return;
    }
    if (!silent) {
      flatLoading.value = true;
    }
    const token = epoch;
    const seq = ++flatSeq;
    try {
      // 保持拉取全量差异文件底表，前端响应式过滤检索，跨树形与平铺模式状态绝对同步
      const res = await ArchiveDiffService.FlatFiles(id, "", "");
      if (token === epoch && seq === flatSeq) flatFiles.value = res ?? [];
    } catch (err) {
      if (token === epoch && seq === flatSeq) error.value = errorMessage(err);
    } finally {
      if (token === epoch && seq === flatSeq) {
        flatLoading.value = false;
      }
    }
  }

  async function openFile(node: ArchiveDiffNode, force = false): Promise<boolean> {
    if (node.isDir) return false;
    if (!force && isDraftDirty.value && draftPath.value !== node.path) {
      pendingSwitchNode.value = node;
      showDraftConfirmModal.value = true;
      return false;
    }
    selectedKeys.value = [node.path.toLowerCase()];
    await openFileDirect(node);
    return true;
  }

  /**
   * 当前过滤与搜索条件下所有有序的差异文件列表
   * 无论在树形模式还是平铺模式下均保持全局一致的路径排序
   * @author flobit
   */
  const navigableFiles = computed<ArchiveDiffNode[]>(() => {
    const q = searchQuery.value.trim().toLowerCase();
    const op = filterOperation.value;

    // 1. 若平铺全量底表已有数据，直接按操作过滤和搜索词过滤
    if (flatFiles.value.length > 0) {
      return flatFiles.value.filter((node) => {
        if (node.isDir) return false;
        const currentOp = diffOperation(node.operation, reverse.value);
        if (op !== "all" && op && currentOp !== op) return false;
        if (q && !node.path.toLowerCase().includes(q)) return false;
        return true;
      });
    }

    // 2. 否则若处于搜索树模式，从 searchedTreeNodes 收集所有匹配叶子节点
    if (q && searchedTreeNodes.value.length > 0) {
      return collectLeavesFromTree(searchedTreeNodes.value, op, reverse.value, q);
    }

    // 3. 兜底：若平铺尚未拉取，从当前 tree 收集已知叶子
    return collectLeavesFromTree(tree.value, op, reverse.value, q);
  });

  const currentFileIndex = computed(() => {
    const currentPath = (file.value?.path || activeNode.value?.path || "").toLowerCase();
    if (!currentPath || navigableFiles.value.length === 0) return -1;
    return navigableFiles.value.findIndex(
      (node) => node.path.toLowerCase() === currentPath
    );
  });

  const hasPreviousFile = computed(() => currentFileIndex.value > 0);
  const hasNextFile = computed(() => {
    const total = navigableFiles.value.length;
    if (total === 0) return false;
    const idx = currentFileIndex.value;
    return idx < total - 1;
  });

  const fileNavIndicator = computed(() => {
    const total = navigableFiles.value.length;
    if (total === 0) return "";
    const current = currentFileIndex.value;
    if (current < 0) return `0 / ${total}`;
    return `${current + 1} / ${total}`;
  });

  /**
   * 导航到指定差异文件，并在确认切换成功后自动展开该文件所在的祖先目录
   * @author flobit
   */
  async function navigateToDiffFile(targetNode: ArchiveDiffNode): Promise<boolean> {
    // 1. 先尝试切换文件（若存在脏草稿会被拦截弹出弹窗，返回 false）
    const success = await openFile(targetNode);
    if (!success) return false;

    // 2. 确认切换成功后，再展开其祖先目录，确保用户取消弹窗时零副作用
    const normalized = targetNode.path.replaceAll("\\", "/").replace(/^\/+/, "");
    const parts = normalized.split("/");
    if (parts.length > 1) {
      let currentDir = "";
      const nextExpanded = new Set(expandedKeys.value.map(String));
      for (let i = 0; i < parts.length - 1; i++) {
        currentDir = currentDir ? `${currentDir}/${parts[i]}` : parts[i];
        nextExpanded.add(currentDir.toLowerCase());
      }
      expandedKeys.value = Array.from(nextExpanded);
    }

    return true;
  }

  async function openPreviousFile(): Promise<boolean> {
    if (!hasPreviousFile.value) return false;
    const target = navigableFiles.value[currentFileIndex.value - 1];
    if (!target) return false;
    return await navigateToDiffFile(target);
  }

  async function openNextFile(): Promise<boolean> {
    if (!hasNextFile.value) return false;
    const nextIdx = currentFileIndex.value < 0 ? 0 : currentFileIndex.value + 1;
    const target = navigableFiles.value[nextIdx];
    if (!target) return false;
    return await navigateToDiffFile(target);
  }

  async function openFileDirect(node: ArchiveDiffNode, silent = false) {
    const id = status.value?.id;
    if (!id || node.isDir) return;
    activeNode.value = node;
    const token = ++fileEpoch;
    const sessionEpoch = epoch;
    const previousDraftPath = draftPath.value;
    const previousDraftText = draftText.value;
    if (!silent) {
      fileLoading.value = true;
      file.value = null;
    }
    fileError.value = "";
    try {
      const result = await ArchiveDiffService.FileDiff(id, node.path);
      if (token === fileEpoch && sessionEpoch === epoch) {
        file.value = result;
        // 方案 B（工作台物理侧锚定）：可编辑底稿始终严格绑定先选中的本地工作区归档文本（result.afterText），彻底杜绝覆盖本地内容
        const baseTxt = result?.afterText ?? "";
        if (!silent || (draftPath.value === previousDraftPath && draftText.value === previousDraftText)) {
          setDraft(node.path, baseTxt, baseTxt);
        }
      }
    } catch (err) {
      if (token === fileEpoch && sessionEpoch === epoch) fileError.value = errorMessage(err);
    } finally {
      if (!silent && token === fileEpoch && sessionEpoch === epoch) {
        fileLoading.value = false;
      }
    }
  }

  async function confirmDraftSaveAndSwitch(): Promise<void> {
    if (draftPath.value && isDraftDirty.value) {
      if (!await mergeText(draftPath.value, draftText.value)) return;
      if (isDraftDirty.value) return;
    }
    showDraftConfirmModal.value = false;
    const nextNode = pendingSwitchNode.value;
    pendingSwitchNode.value = null;
    if (nextNode) {
      await openFile(nextNode, true);
    }
  }

  function confirmDraftDiscardAndSwitch(): void {
    discardDraft();
    showDraftConfirmModal.value = false;
    const nextNode = pendingSwitchNode.value;
    pendingSwitchNode.value = null;
    if (nextNode) {
      void openFile(nextNode, true);
    }
  }

  function cancelDraftSwitch(): void {
    pendingSwitchNode.value = null;
    showDraftConfirmModal.value = false;
  }

  async function mergeSelected() {
    const id = status.value?.id;
    const paths = Array.from(selectedPaths.value);
    if (!id || paths.length === 0 || merging.value) return null;
    const token = epoch;
    merging.value = true;
    try {
      const res = await ArchiveDiffService.MergeFiles(id, paths);
      if (token !== epoch) return res;
      if (res && res.mergedPaths && res.mergedPaths.length > 0) {
        await useEditorStore().refreshMergedFiles(res.mergedPaths);
        if (token !== epoch) return res;
        deselectPaths(res.mergedPaths);
        if (activeNode.value && res.mergedPaths.includes(activeNode.value.path)) {
          await openFileDirect(activeNode.value, true);
        }
        if (token !== epoch) return res;
        await refreshCurrentView();
        await refreshStatus(token, id);
      }
      return res;
    } finally {
      if (token === epoch) merging.value = false;
    }
  }

  async function mergeSingleFile(path: string): Promise<boolean> {
    const id = status.value?.id;
    if (!id || !path || merging.value) return false;
    const token = epoch;
    merging.value = true;
    try {
      const res = await ArchiveDiffService.MergeFiles(id, [path]);
      if (token !== epoch) return false;
      if (res && res.successCount > 0) {
        await useEditorStore().refreshMergedFiles([path]);
        if (token !== epoch) return false;
        deselectPaths([path]);
        if (activeNode.value && activeNode.value.path === path) {
          await openFileDirect(activeNode.value, true);
        }
        if (token !== epoch) return false;
        await refreshCurrentView(path);
        await refreshStatus(token, id);
        return true;
      }
      return false;
    } finally {
      if (token === epoch) merging.value = false;
    }
  }

  async function mergeText(path: string, text: string): Promise<boolean> {
    const id = status.value?.id;
    if (!id || !path || merging.value) return false;
    const token = epoch;
    merging.value = true;
    try {
      await ArchiveDiffService.MergeText(id, path, text);
      if (token !== epoch) return false;
      await useEditorStore().refreshMergedFiles([path]);
      if (token !== epoch) return false;
      if (draftPath.value === path) originalDraftText.value = text;
      if (activeNode.value?.path === path && !isDraftDirty.value) {
        await openFileDirect(activeNode.value, true);
      }
      if (token !== epoch) return false;
      await refreshCurrentView(path);
      await refreshStatus(token, id);
      return true;
    } finally {
      if (token === epoch) merging.value = false;
    }
  }

  async function refreshStatus(token: number, id: string) {
    if (token !== epoch) return;
    const result = await ArchiveDiffService.Status(id);
    if (token === epoch && result) status.value = result;
  }

  async function cancel() {
    starting.value = false;
    merging.value = false;
    rootLoading.value = false;
    flatLoading.value = false;
    const id = status.value?.id;
    const token = ++epoch;
    fileEpoch++;
    fileLoading.value = false;
    clearTimeout(timer);
    if (id) {
      if (status.value) status.value = { ...status.value, state: "cancelled" };
      try { await ArchiveDiffService.Cancel(id); }
      catch (err) { if (token === epoch) error.value = errorMessage(err); }
    }
  }

  async function onArchiveReloaded() {
    if (otherPath.value) {
      const cancelled = cancel();
      const token = epoch;
      await cancelled;
      if (token !== epoch) return;
      await refresh();
    } else {
      clearResult();
    }
  }

  Events.On("archive:opened", close);
  Events.On("archive:closed", close);
  Events.On("archive:reloaded", () => {
    void onArchiveReloaded();
  });

  return {
    visible, otherPath, reverse, status, tree, file, activeNode, displayedFile, selectedKeys, expandedKeys,
    starting, selecting, busy, fileLoading, rootLoading, error, fileError, counts, totalCount,
    viewMode, listMode, filterOperation, searchQuery, flatFiles, flatLoading, displayedTree,
    lineWrapping,
    selectedPaths, merging, draftPath, draftText, originalDraftText, isDraftDirty,
    showDraftConfirmModal, pendingSwitchNode,
    open, close, selectArchive, refresh, loadRootTree, loadChildren, openFile, openFileDirect, cancel,
    toggleSelect, selectAll, deselectAll, clearSelection, isSelected, isDirSelected, isDirIndeterminate, getDirSelectionState,
    toggleSelectDir, getMatchingFilesUnderDir, searchTree, clearSearch, collapseAll, setFilterOperation, toggleReverse,
    loadFlatFiles, refreshCurrentView, mergeSelected, mergeSingleFile, mergeText,
    setDraft, updateDraftText, discardDraft,
    navigableFiles, currentFileIndex, hasPreviousFile, hasNextFile, fileNavIndicator,
    openPreviousFile, openNextFile,
    confirmDraftSaveAndSwitch, confirmDraftDiscardAndSwitch, cancelDraftSwitch,
  };
});
