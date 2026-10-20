<script setup lang="ts">
import { computed, onBeforeUnmount, ref, toRef, watch } from "vue";
import { NButton, NEmpty, NModal, NSelect, NSlider, NSpin, useMessage } from "naive-ui";
import { FileGUIService } from "../../../bindings/pvfine/services";
import type { SkillTreeDocument, SkillTreeNode, SkillTreeRegistration } from "../../../bindings/pvfine/services/models";
import ImageThumbnail from "../ImageThumbnail.vue";
import { useFileGUIStore } from "../../stores/fileGUI";
import { useEditorStore } from "../../stores/editor";
import { skillJobNames } from "../../gui/skill";
import { skillBranchNames, skillTreeEdge, groupSkillTreeCells, skillTreeRowLevels } from "../../gui/skillTree";

const props = defineProps<{ show: boolean }>();
const emit = defineEmits<{ (e: "update:show", value: boolean): void }>();
const gui = useFileGUIStore();
const editor = useEditorStore();
const message = useMessage();
const registrations = ref<SkillTreeRegistration[]>([]);
const doc = ref<SkillTreeDocument | null>(null);
const job = toRef(gui.skillTreeSelection, "job");
const kind = toRef(gui.skillTreeSelection, "kind");
const branch = toRef(gui.skillTreeSelection, "branch");
const selected = ref<SkillTreeNode | null>(null);
const loading = ref(false);
const error = ref("");
const zoom = ref(130);
const opening = ref(false);
let request = 0;
const jobOptions = computed(() => [...new Set(registrations.value.map(r => r.job))].map(value => ({value, label: skillJobNames[value.replace(/[\[\] ]/g, "")] || value})));
const kinds = computed(() => registrations.value.filter(r => r.job === job.value).map(r => ({value: r.kind, label: r.kind === "sp" ? "SP · 技能" : "TP · 特性技能"})));
const branches = computed(() => (doc.value?.branches ?? []).map(b => ({value: b.name, label: skillBranchNames[b.name] || b.name})));
const nodes = computed(() => doc.value?.branches?.find(b => b.name === branch.value)?.nodes ?? []);
const rowLevels = computed(() => skillTreeRowLevels(nodes.value, kind.value));
const cells = computed(() => groupSkillTreeCells(nodes.value));
const selectedCell = computed(() => cells.value.find(cell => cell.nodes.includes(selected.value!)));
function cellNode(cell: { nodes: SkillTreeNode[] }) {
  return selected.value && cell.nodes.includes(selected.value) ? selected.value : cell.nodes[0]!;
}
const width = computed(() => Math.max(564, ...nodes.value.map(n => n.x + 47)));
const height = computed(() => Math.max(268, ...nodes.value.map(n => n.y + 67)));
const rows = computed(() => Array.from({length: Math.ceil(height.value / 67)}, (_, i) => i));
const edges = computed(() => {
  const byID = new Map(nodes.value.map(n => [n.id, n]));
  return nodes.value.flatMap((source, index) => (source.next ?? []).flatMap((id, order) => {
    const target = byID.get(id);
    return target && (target.x !== source.x || target.y !== source.y) ? [{key: `${index}:${order}`, source, target, path: skillTreeEdge(source, target)}] : [];
  }));
});
function linked(n: SkillTreeNode) {
  return selected.value && (selected.value.id === n.id || selected.value.next?.includes(n.id) || n.next?.includes(selected.value.id));
}
async function scan() {
  const id = ++request, epoch = gui.epoch;
  loading.value = true; error.value = ""; doc.value = null; selected.value = null;
  try {
    const result = await FileGUIService.ListSkillTrees();
    if (id !== request || epoch !== gui.epoch) return;
    registrations.value = result ?? [];
    if (!registrations.value.some(r => r.job === job.value)) job.value = registrations.value[0]?.job ?? null;
    if (!kinds.value.some(k => k.value === kind.value)) kind.value = kinds.value[0]?.value ?? "sp";
    if (job.value) await load();
  } catch (e) {if (id === request) error.value = String(e);}
  finally {if (id === request) loading.value = false;}
}
async function load() {
  if (!props.show || !job.value) return;
  const id = ++request, epoch = gui.epoch;
  loading.value = true; error.value = ""; doc.value = null; selected.value = null;
  try {
    const result = await FileGUIService.ReadSkillTree(job.value, kind.value);
    if (id !== request || epoch !== gui.epoch) return;
    doc.value = result;
    if (!result?.branches?.some(b => b.name === branch.value)) branch.value = result?.branches?.[0]?.name ?? null;
  } catch (e) {if (id === request) error.value = String(e);}
  finally {if (id === request) loading.value = false;}
}
function changeJob(value: string) {
  job.value = value;
  if (!kinds.value.some(k => k.value === kind.value)) kind.value = kinds.value[0]?.value ?? "sp";
  void load();
}
async function openSkill() {
  const skill = selected.value;
  if (!skill || skill.fileIndex < 0 || opening.value || editor.saving) return;
  const epoch = gui.epoch;
  opening.value = true;
  try {
    await editor.openFile(skill.fileIndex);
    if (epoch === gui.epoch) emit("update:show", false);
  } catch (e) {message.error(String(e));}
  finally {opening.value = false;}
}
async function openTreeSource(event: MouseEvent | KeyboardEvent) {
  if (!event.metaKey && !event.ctrlKey) return;
  event.preventDefault();
  const tree = doc.value;
  if (!tree || tree.fileIndex < 0 || opening.value || editor.saving) return;
  const epoch = gui.epoch;
  opening.value = true;
  try {
    await editor.openFile(tree.fileIndex);
    if (epoch === gui.epoch) emit("update:show", false);
  } catch (e) {message.error(String(e));}
  finally {opening.value = false;}
}
watch(() => props.show, show => {if (show) void scan(); else request++;}, {immediate: true});
watch(branch, () => {selected.value = null;});
watch(() => gui.epoch, () => {request++; doc.value = null; registrations.value = []; loading.value = false; emit("update:show", false);});
onBeforeUnmount(() => {request++;});
</script>

<template>
  <NModal :show="show" preset="card" title="技能树" style="width: 1450px; max-width: 96vw" :content-style="{ padding: '0' }" @update:show="emit('update:show', $event)">
    <div class="tree-toolbar">
      <NSelect :value="job" :options="jobOptions" placeholder="选择职业" aria-label="职业" style="width: 200px" :disabled="loading" @update:value="changeJob" />
      <NSelect v-model:value="branch" :options="branches" placeholder="选择转职" aria-label="转职" style="width: 180px" :disabled="loading" />
      <div class="tree-kinds"><NButton v-for="k in kinds" :key="k.value" :type="kind === k.value ? 'primary' : 'default'" size="small" :disabled="loading" @click="kind = k.value; load()">{{ k.label }}</NButton></div>
      <span class="readonly">只读</span>
      <NButton size="small" :disabled="loading" @click="scan">重新读取</NButton>
    </div>
    <div v-if="error" class="tree-error" role="alert">{{ error }} <NButton size="small" @click="scan">重试</NButton></div>
    <NSpin :show="loading">
      <div class="tree-layout">
        <main class="tree-scroll" aria-label="技能树画布">
          <template v-if="doc">
            <section class="common-skills" aria-label="通用技能">
              <span class="common-label">通用</span>
              <button v-for="(n, i) in doc.common" :key="i" class="common-node" :class="{ selected: selected === n }" :title="`${n.name} #${n.id}`" @click="selected = n">
                <ImageThumbnail :reference="n.icon" :size="36" show-fallback /><small>{{ n.name }}</small>
              </button>
              <span v-if="!doc.common?.length" class="muted">无通用技能</span>
            </section>
            <div v-if="nodes.length" class="canvas-space" :style="{ width: `${(width + 40) * zoom / 100}px`, height: `${height * zoom / 100}px` }">
              <div class="tree-canvas" :style="{ width: `${width + 40}px`, height: `${height}px`, transform: `scale(${zoom / 100})` }">
                <div v-for="row in rows" :key="row" class="tree-row" :class="{ alternate: row % 2 === 0 }" :style="{ top: `${row * 67}px` }"><span v-if="kind === 'sp'">{{ rowLevels.get(row) }}</span></div>
                <svg class="tree-edges" :width="width" :height="height" style="left: 40px" aria-label="技能后继关系">
                  <defs><marker id="skill-tree-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 8 4 L 0 8 z" fill="context-stroke" /></marker></defs>
                  <path v-for="edge in edges" :key="edge.key" :d="edge.path" fill="none" :stroke="selected && (edge.source.id === selected.id || edge.target.id === selected.id) ? '#e8b65e' : '#87744f'" stroke-width="1.7" marker-end="url(#skill-tree-arrow)" />
                </svg>
                <button v-for="cell in cells" :key="cell.key" class="tree-node" :class="{ selected: cell.nodes.includes(selected!), linked: cell.nodes.some(linked) }" :style="{ left: `${cellNode(cell).x + 40}px`, top: `${cellNode(cell).y + 6}px` }" :title="cell.nodes.map(n => `${n.name} #${n.id}`).join('\n')" :aria-label="`${cellNode(cell).name}，技能 ${cellNode(cell).id}${cell.nodes.length > 1 ? `，同位置共 ${cell.nodes.length} 个技能` : ''}`" @click="selected = cellNode(cell)">
                  <ImageThumbnail :reference="cellNode(cell).icon" :size="32" show-fallback /><small><span>{{ cellNode(cell).name }}</span></small>
                  <span v-if="cell.nodes.length > 1" class="overlap-count">{{ cell.nodes.length }}</span>
                </button>
              </div>
            </div>
            <NEmpty v-else description="当前分支未配置技能" class="tree-empty" />
          </template>
          <NEmpty v-else-if="!loading && !error" description="归档中没有注册技能树" class="tree-empty" />
        </main>
        <aside class="tree-detail">
          <template v-if="selected">
            <div v-if="selectedCell && selectedCell.nodes.length > 1" class="overlap-picker">
              <div class="overlap-title">同位置技能（{{ selectedCell.nodes.length }}）</div>
              <button v-for="(n, i) in selectedCell.nodes" :key="i" :class="{ active: selected === n }" :aria-pressed="selected === n" @click="selected = n"><span>#{{ n.id }}</span>{{ n.name }}</button>
            </div>
            <div class="detail-title"><ImageThumbnail :reference="selected.icon" :size="48" show-fallback /><div><strong>{{ selected.name }}</strong><span>技能 #{{ selected.id }}</span></div></div>
            <p class="skill-explain">{{ selected.explain || '暂无技能描述' }}</p>
            <NButton block :disabled="selected.fileIndex < 0 || opening || editor.saving" :loading="opening" @click="openSkill">打开技能文件</NButton>
          </template>
          <NEmpty v-else description="点击技能查看详情与关联" />
          <div class="tree-notes"><p><template v-if="kind === 'sp'">左侧数字为该行最左侧技能的学习等级。</template>箭头表示文件定义的后继关系。</p><p>图标需在设置中配置对应客户端的 NPK 资源。</p></div>
          <details v-if="doc?.issues?.length" class="tree-issues"><summary>数据提示（{{ doc.issues.length }}）</summary><p v-for="(issue, i) in doc.issues" :key="i">{{ issue }}</p></details>
        </aside>
      </div>
    </NSpin>
    <footer class="tree-footer"><button class="source-path" :disabled="!doc || opening || editor.saving" title="Cmd / Ctrl + 单击，在编辑器打开技能树文件" @click="openTreeSource" @keydown.enter="openTreeSource">{{ doc?.path || '从当前 PVF 读取' }}</button><span>{{ nodes.length }} 个技能 · {{ edges.length }} 条连线</span><label>缩放 {{ zoom }}%</label><NSlider v-model:value="zoom" :min="80" :max="240" :step="10" style="width: 110px" aria-label="画布缩放" /></footer>
  </NModal>
</template>

<style scoped>
.tree-toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; padding: 12px 18px; border-bottom: 1px solid var(--pvf-border-subtle); }
.tree-kinds { display: flex; gap: 6px; } .readonly { margin-left: auto; color: var(--pvf-text-secondary); font-size: 12px; }
.tree-layout { display: grid; grid-template-columns: minmax(0, 1fr) 270px; height: min(72vh, 900px); min-height: 300px; }
.tree-scroll { overflow: auto; min-width: 0; background: #171a1d; }
.common-skills { display: flex; gap: 10px; align-items: center; min-width: max-content; padding: 18px 24px; border-bottom: 1px solid #454239; background: #202429; }
.common-label { color: #cab789; font-size: 13px; padding-right: 10px; writing-mode: vertical-rl; letter-spacing: 5px; }
.common-node { width: 62px; display: flex; flex-direction: column; align-items: center; gap: 7px; background: transparent; color: #d8d8d4; border: 1px solid transparent; padding: 5px 2px; cursor: pointer; }
.common-node small { font-size: 11px; width: 100%; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.canvas-space { position: relative; margin-top: 14px; }
.tree-canvas { position: relative; transform-origin: top left; }
.tree-row { position: absolute; width: 100%; height: 67px; color: #9b967e; font-size: 10px; }
.tree-row.alternate { background: #202428; } .tree-row > span { position: absolute; top: 18px; left: 12px; }
.tree-edges { position: absolute; top: 6px; pointer-events: none; }
.tree-node { position: absolute; width: 34px; height: 56px; display: flex; flex-direction: column; align-items: center; padding: 0; border: 1px solid #666356; border-radius: 2px; background: #12171b; color: #c2bca9; cursor: pointer; }
.overlap-count { position: absolute; right: -6px; top: -7px; min-width: 13px; height: 13px; padding: 0 2px; box-sizing: border-box; border-radius: 7px; background: #d4ad63; color: #211e18; font-size: 9px; line-height: 13px; font-weight: 700; }
.overlap-picker { margin-bottom: 20px; } .overlap-title { margin-bottom: 8px; font-size: 12px; color: var(--pvf-text-secondary); }
.overlap-picker button { display: flex; align-items: baseline; gap: 8px; width: 100%; padding: 8px; background: transparent; border: 1px solid var(--pvf-border-subtle); color: var(--pvf-text-primary); text-align: left; cursor: pointer; font: inherit; font-size: 12px; }
.overlap-picker button + button { border-top: 0; } .overlap-picker button.active { background: var(--pvf-primary-soft); color: var(--pvf-primary); } .overlap-picker button span { color: var(--pvf-text-secondary); font-size: 11px; }
.tree-node small { box-sizing: border-box; width: 100%; min-width: 0; min-height: 0; flex: 1; display: flex; align-items: center; line-height: 10px; font-size: 8px; padding-top: 2px; }
.tree-node small > span { width: 100%; min-width: 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; text-align: center; } .tree-node.linked { border-color: #af9158; }
.tree-node:hover, .tree-node.selected, .common-node:hover, .common-node.selected { border-color: #f1c779; outline: 1px solid #c69c4c; color: #ffe0a4; }
button:focus-visible { outline: 2px solid #f1c779; outline-offset: 3px; }
.tree-detail { overflow: auto; border-left: 1px solid var(--pvf-border-subtle); padding: 22px 16px; }
.detail-title { display: flex; align-items: center; gap: 12px; } .detail-title > div { display: flex; flex-direction: column; gap: 6px; } .detail-title span { font-size: 12px; color: var(--pvf-text-secondary); }
.skill-explain { margin: 22px 0; font-size: 13px; line-height: 1.75; white-space: pre-wrap; overflow-wrap: anywhere; }
.tree-notes { margin-top: 28px; color: var(--pvf-text-secondary); font-size: 12px; line-height: 1.7; } .tree-issues { font-size: 12px; overflow-wrap: anywhere; } .tree-issues summary { cursor: pointer; color: #ba9450; }
.tree-footer { display: flex; align-items: center; gap: 16px; padding: 10px 18px; border-top: 1px solid var(--pvf-border-subtle); color: var(--pvf-text-secondary); font-size: 12px; } .source-path { flex: 1; min-width: 0; overflow-wrap: anywhere; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer; user-select: text; }
.source-path:hover:not(:disabled) { text-decoration: underline; }
.tree-error { padding: 12px 18px; color: var(--pvf-danger, #c95151); } .tree-empty { margin: 70px 0; } .muted { color: #aaa; }
@media (max-width: 850px) { .tree-layout { grid-template-columns: minmax(0, 1fr) 210px; } .tree-footer { flex-wrap: wrap; } }
</style>
