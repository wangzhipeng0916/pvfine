<script setup lang="ts">
import { computed } from "vue";
import {
  NAlert,
  NButton,
  NCheckbox,
  NDivider,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NProgress,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
  NTooltip,
} from "naive-ui";
import { ArrowClockwise20Regular, ArrowDownload20Regular } from "@vicons/fluent";
import {
  bytesLabel,
  coverageLabel,
  extractionFormOptions,
  extractionLayoutOptions,
  extractionStateLabels,
  stateTotal,
  unresolvedCount,
  useExtractionStore,
} from "../stores/extraction";

const extraction = useExtractionStore();
const rows = computed(() => extraction.preview?.categories ?? []);
// 空选中集在 Go 侧就是"全部类别"，所以全选状态不能写成把每个 id 都勾上。
const allChecked = computed(() => extraction.checked.length === 0 || extraction.checked.length >= rows.value.length);
const checkedRows = computed(() =>
  allChecked.value ? rows.value : rows.value.filter((row) => extraction.checked.includes(row.id)),
);
const checkedFiles = computed(() => checkedRows.value.reduce((sum, row) => sum + row.files, 0));
const checkedBytes = computed(() => checkedRows.value.reduce((sum, row) => sum + row.bytes, 0));
const unresolved = computed(() => unresolvedCount(extraction.preview));
const refTotal = computed(() => stateTotal(extraction.preview));
const stateRows = computed(() =>
  Object.entries(extraction.preview?.states ?? {})
    .map(([name, count]) => [name, count ?? 0] as [string, number])
    .sort((left, right) => right[1] - left[1]),
);
const seedShown = computed(() => extraction.allSeeds.slice(0, 6));
const presetOptions = computed(() => [
  { label: "不用预设（自己勾类别）", value: "" },
  ...extraction.presets.map((item) => ({ label: item.name, value: item.id })),
]);
const stateType = (name: string): "success" | "warning" | "error" | "default" => {
  if (name === "resolved") return "success";
  if (name === "missing" || name === "unmodeled") return "warning";
  if (name === "unknown") return "error";
  return "default";
};
</script>

<template>
  <NModal
    :show="extraction.visible"
    preset="card"
    :title="extraction.title"
    class="extraction-modal"
    :style="{ width: '680px', maxWidth: 'calc(100vw - 32px)', maxHeight: 'calc(100vh - 48px)' }"
    :content-style="{ overflowY: 'auto', minHeight: 0 }"
    :z-index="3000"
    :closable="!extraction.running"
    :mask-closable="!extraction.running"
    :close-on-esc="!extraction.running"
    @update:show="(show: boolean) => { if (!show) extraction.close(); }"
  >
    <NSpace vertical :size="12">
      <div class="extraction-seeds">
        <NText strong>种子</NText>
        <NSpace :size="6" inline>
          <NTag v-for="seed in seedShown" :key="seed" size="small" :bordered="false" :title="seed">
            {{ seed.split("/").pop() }}
          </NTag>
        </NSpace>
        <NText v-if="extraction.allSeeds.length > seedShown.length" depth="3">
          …共 {{ extraction.allSeeds.length }} 个
        </NText>
        <NText v-else-if="!extraction.allSeeds.length" depth="3">未选择</NText>
      </div>

      <div class="extraction-extra">
        <NInput
          v-model:value="extraction.extraSeeds"
          size="small"
          type="textarea"
          :autosize="{ minRows: 1, maxRows: 3 }"
          placeholder="追加种子，每行一个：条目路径、关系名:ID（如 怪物:10021），或粘贴 lst 片段行（ID 与路径之间是制表符）"
          :disabled="extraction.running"
          @keydown.enter.prevent="extraction.acceptExtraSeeds()"
        />
        <NButton
          size="small"
          :disabled="extraction.running || !extraction.extraSeeds.trim()"
          @click="extraction.acceptExtraSeeds()"
        >
          加入
        </NButton>
        <NSelect
          v-model:value="extraction.listRelation"
          size="small"
          class="extraction-relation"
          clearable
          filterable
          placeholder="清单所属表（可选）：粘贴纯 ID 列表时按它解析"
          :options="extraction.relationOptions"
          :disabled="extraction.running || extraction.relationOptions.length === 0"
        />
        <NText depth="3" class="extraction-extra-hint">
          片段行按"从左往右第一个能命中的列"取路径，不必切成两列；路径列在这份归档里找不到就报错，不会退回按同一行的数字猜文件。
          纯 ID 没选表时只在归属唯一时才认；86 实测抽样 660 个 ID 里 84.7% 被多张表同时认领，写错会直接报候选关系。
        </NText>
      </div>

      <NSpin :show="extraction.preparing">
        <div class="extraction-toolbar">
          <NText depth="3">
            勾选要导出的类别（闭包 {{ extraction.preview?.closureCount ?? 0 }} 个条目）
          </NText>
          <NText v-if="allChecked && rows.length" depth="3">当前为全部类别</NText>
          <NButton
            v-else
            size="small"
            quaternary
            :disabled="extraction.running || extraction.preparing || !extraction.preview"
            @click="extraction.selectAllCategories()"
          >
            全部选中
          </NButton>
        </div>
        <div class="extraction-categories">
          <label v-for="row in rows" :key="row.id" class="extraction-category">
            <NCheckbox
              :checked="allChecked || extraction.checked.includes(row.id)"
              :disabled="extraction.running || extraction.preparing"
              @update:checked="(on: boolean) => extraction.toggleCategory(row.id, on)"
            />
            <span class="extraction-category-name" :title="row.name">{{ row.name }}</span>
            <span class="extraction-category-count">{{ row.files }} 个 / {{ bytesLabel(row.bytes) }}</span>
            <NTooltip v-if="row.autoFiles">
              <template #trigger>
                <NTag size="tiny" type="info" :bordered="false">补入 {{ row.autoFiles }}</NTag>
              </template>
              为保持闭包而自动落盘的条目，它们在 _external/ 下
            </NTooltip>
          </label>
          <NText v-if="!rows.length && !extraction.preparing" depth="3">本次闭包没有可分类条目</NText>
        </div>
      </NSpin>

      <div class="extraction-summary">
        <NText>
          落盘 {{ extraction.preview?.exportedCount ?? 0 }} 个文件
          （主集 {{ extraction.preview?.mainCount ?? 0 }}／补链 {{ extraction.preview?.ancestorCount ?? 0 }}／补入 {{ extraction.preview?.externalCount ?? 0 }}）
        </NText>
        <NText v-if="checkedFiles < (extraction.preview?.exportedCount ?? 0)" depth="3">
          勾选项本身 {{ checkedFiles }} 个 / {{ bytesLabel(checkedBytes) }}，其余为补链与闭包补入
        </NText>
      </div>

      <NSpace :size="6">
        <NTag
          v-for="[name, count] in stateRows"
          :key="name"
          size="small"
          :type="stateType(name)"
          :bordered="false"
        >
          {{ extractionStateLabels[name] ?? name }} {{ count }}
        </NTag>
        <NText v-if="extraction.preview && unresolved && refTotal" depth="3">
          引用不确定率 {{ coverageLabel(unresolved / refTotal) }}
        </NText>
      </NSpace>
      <div class="extraction-coverage">
        <NText depth="3">边目录词汇消费率</NText>
        <NProgress
          type="line"
          indicator-placement="inside"
          :percentage="Math.round((extraction.preview?.vocabCoverage ?? 0) * 1000) / 10"
          :height="14"
        />
      </div>

      <NDivider style="margin: 4px 0" />

      <div class="extraction-options">
        <NSelect
          :value="extraction.preset"
          size="small"
          :options="presetOptions"
          :disabled="extraction.running"
          class="extraction-option-grow"
          @update:value="extraction.applyPreset($event)"
        />
        <NSelect
          v-model:value="extraction.layout"
          size="small"
          :options="extractionLayoutOptions"
          :disabled="extraction.running"
          class="extraction-option-grow"
        />
        <NSelect
          v-model:value="extraction.form"
          size="small"
          :options="extractionFormOptions"
          :disabled="extraction.running"
          class="extraction-option-grow"
        />
        <NInputNumber
          v-model:value="extraction.maxEntries"
          size="small"
          :min="0"
          :show-button="false"
          placeholder="条目上限"
          :disabled="extraction.running"
          class="extraction-option-number"
        />
        <NTooltip>
          <template #trigger>
            <span class="extraction-switch">
              <NText depth="3">严格子集</NText>
              <NSwitch v-model:value="extraction.strict" size="small" :disabled="extraction.running" />
            </span>
          </template>
          开启后只导勾选项，被引用而未导出的条目记为悬空引用而不是补进来
        </NTooltip>
        <NTooltip>
          <template #trigger>
            <span class="extraction-switch">
              <NText depth="3">登记建议</NText>
              <NSwitch v-model:value="extraction.lstSuggest" size="small" :disabled="extraction.running" />
            </span>
          </template>
          额外产出 (5)登记建议.txt：被引用却没登记进 .lst 的条目（默认关，登记会改变游戏加载内容）
        </NTooltip>
        <NTooltip>
          <template #trigger>
            <span class="extraction-switch">
              <NText depth="3">图片容器</NText>
              <NSwitch
                v-model:value="extraction.copyAssets"
                size="small"
                :disabled="extraction.running || !(extraction.preview?.assetContainers?.length ?? 0)"
              />
            </span>
          </template>
          把闭包引用到的 NPK 容器整份拷进产物的 npk/ 目录；默认只出 (6)资源与NPK对照.txt 列出归属。
          预览会先算清楚要拷几个、多大，体积大就先看数字再决定。
        </NTooltip>
      </div>

      <NText v-if="extraction.activePreset" depth="3" class="extraction-preset-note">
        {{ extraction.activePreset.description }}
        <template v-if="extraction.activePreset.seedHint">
          <br />
          种子写法：{{ extraction.activePreset.seedHint }}
        </template>
      </NText>

      <NAlert v-if="(extraction.preview?.assetTotal ?? 0) > 0" type="info" :show-icon="false">
        资源：引用 {{ extraction.preview?.assetTotal }} 个图片，其中 {{ extraction.preview?.assetNpkMatched }}
        个能归到 {{ extraction.preview?.assetContainers?.length ?? 0 }} 个 NPK 容器（约
        {{ ((extraction.preview?.assetNpkBytes ?? 0) / 1048576).toFixed(1) }} MiB），
        {{ extraction.preview?.assetDynamic ?? 0 }} 个是 %d/通配模板、只能由客户端运行时决定。
      </NAlert>
      <NAlert v-else-if="extraction.preview && !extraction.preview.assetTotal" type="info" :show-icon="false">
        资源：这批内容没有引用任何客户端图片。
      </NAlert>

      <NAlert v-if="!extraction.strict && (extraction.preview?.externalCount ?? 0) > 0" type="info" :show-icon="false">
        闭包模式下取消勾选不会减少落盘量：被引用到的条目仍会以 _external/ 补入。要"只要勾选项"请打开严格子集。
      </NAlert>
      <NAlert v-if="(extraction.preview?.danglingCount ?? 0) > 0" type="warning">
        严格子集下有 {{ extraction.preview?.danglingCount }} 条引用指向未导出的条目，产物不完整。
      </NAlert>
      <NAlert
        v-for="warning in extraction.preview?.warnings ?? []"
        :key="warning"
        type="warning"
      >
        {{ warning }}
      </NAlert>
      <NAlert v-if="extraction.hint" type="info" :show-icon="false">{{ extraction.hint }}</NAlert>
      <NAlert v-if="extraction.error" type="error">{{ extraction.error }}</NAlert>

      <NText v-if="extraction.preview" depth="3" class="extraction-dir">
        落点由系统目录对话框选择；产物含 manifest.json、report/ 四份清单与 _registries/ 登记表片段。
      </NText>
    </NSpace>

    <template #footer>
      <NSpace justify="end">
        <NButton :disabled="extraction.running" @click="extraction.close()">取消</NButton>
        <NButton
          quaternary
          :disabled="extraction.running || extraction.preparing || !extraction.preview"
          @click="extraction.refresh()"
        >
          <template #icon>
            <NIcon><ArrowClockwise20Regular /></NIcon>
          </template>
          重新准备
        </NButton>
        <NButton
          type="primary"
          :loading="extraction.running"
          :disabled="!extraction.canExport"
          @click="extraction.execute()"
        >
          <template #icon>
            <NIcon><ArrowDownload20Regular /></NIcon>
          </template>
          选择目录并提取
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.extraction-extra {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  flex-wrap: wrap;
}
.extraction-extra-hint {
  width: 100%;
  font-size: 12px;
  line-height: 1.5;
}
.extraction-relation {
  width: 100%;
}
.extraction-extra :deep(.n-input) {
  flex: 1;
  min-width: 0;
}
.extraction-seeds,
.extraction-summary,
.extraction-toolbar,
.extraction-coverage {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}
.extraction-toolbar {
  justify-content: space-between;
}
.extraction-categories {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 4px 12px;
  max-height: 180px;
  overflow-y: auto;
  margin-top: 6px;
}
.extraction-category {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.extraction-category-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.extraction-category-count {
  margin-left: auto;
  color: var(--n-text-color-3, #888);
  font-size: 12px;
  white-space: nowrap;
}
.extraction-options {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;}
.extraction-option-grow {
  min-width: 150px;
  flex: 1;
}
.extraction-option-number {
  width: 120px;
}
.extraction-switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.extraction-coverage :deep(.n-progress) {
  flex: 1;
  min-width: 120px;
}
.extraction-dir {
  display: flex;
  align-items: center;
  gap: 8px;
}
.extraction-dir :deep(.n-input) {
  flex: 1;
  min-width: 0;
}
.extraction-modal :deep(.n-card__content) {
  min-height: 0;
  overflow-y: auto;
}

.extraction-preset-note {
  display: block;
  margin: 2px 0 6px;
  line-height: 1.5;
}
</style>
