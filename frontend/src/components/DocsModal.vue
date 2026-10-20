<script setup lang="ts">
import { computed, ref } from "vue";
import { NButton, NIcon, NModal, NTabPane, NTabs } from "naive-ui";
import { ArrowUpRight24Regular } from "@vicons/fluent";
import { Browser } from "@wailsio/runtime";
import { extractMarkdownSection, plainMarkdown } from "../docs";
import changelogLocalRaw from "../../../CHANGELOG.fork.md?raw";
import changelogUpstreamRaw from "../../../CHANGELOG.md?raw";
import readmeRaw from "../../../README.md?raw";

defineProps<{ show: boolean }>();
const emit = defineEmits<{ (e: "update:show", value: boolean): void }>();

const activeTab = ref("features");

// README 的简介与核心特性拼起来才是「功能列表与介绍」：只给列表会丢掉技术栈前提，
// 只给简介又看不到具体能力。
const features = computed(() =>
  plainMarkdown(
    [
      extractMarkdownSection(readmeRaw, "项目简介"),
      extractMarkdownSection(readmeRaw, "核心特性"),
    ].join("\n\n")
  )
);
const upstreamLog = computed(() => plainMarkdown(changelogUpstreamRaw));
const localLog = computed(() => plainMarkdown(changelogLocalRaw));

const upstreamCount = computed(
  () => (changelogUpstreamRaw.match(/^## \[.*/gm) ?? []).length
);
const localCount = computed(() => (changelogLocalRaw.match(/^## \[.*/gm) ?? []).length);

const links = [
  { label: "上游仓库", url: "https://github.com/dof-dev/pvfine" },
  { label: "上游 Releases", url: "https://github.com/dof-dev/pvfine/releases" },
  { label: "本分支提交", url: "https://github.com/wangzhipeng0916/pvfine/commits/main" },
];

async function openLink(url: string): Promise<void> {
  try {
    await Browser.OpenURL(url);
  } catch {
    window.open(url, "_blank");
  }
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    title="文档与更新日志"
    style="width: 900px; max-width: 94vw"
    :content-style="{ padding: '0 20px 16px' }"
    @update:show="emit('update:show', $event)"
  >
    <NTabs v-model:value="activeTab" type="line" animated pane-style="padding-top: 8px">
      <NTabPane name="features" tab="功能列表与介绍">
        <pre class="docs-body">{{ features }}</pre>
      </NTabPane>
      <NTabPane name="upstream" :tab="`上游更新日志 · ${upstreamCount} 版`">
        <pre class="docs-body">{{ upstreamLog }}</pre>
      </NTabPane>
      <NTabPane name="local" :tab="`本分支更新日志 · ${localCount} 条`">
        <pre class="docs-body">{{ localLog }}</pre>
      </NTabPane>
    </NTabs>
    <div class="docs-footer">
      <span class="docs-footer-note">内容随构建嵌入，离线可读；需要最新记录请更新到新版本</span>
      <div class="docs-footer-links">
        <NButton
          v-for="link in links"
          :key="link.url"
          size="tiny"
          secondary
          @click="openLink(link.url)"
        >
          <template #icon><NIcon :size="13"><ArrowUpRight24Regular /></NIcon></template>
          {{ link.label }}
        </NButton>
      </div>
    </div>
  </NModal>
</template>

<style scoped>
.docs-body {
  max-height: 58vh;
  overflow-y: auto;
  margin: 0;
  padding: 12px 14px;
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  background: var(--pvf-surface-subtle);
  color: var(--pvf-text-primary);
  font-family: inherit;
  font-size: 13px;
  line-height: 1.75;
  white-space: pre-wrap;
  word-break: break-word;
}

.docs-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.docs-footer-note {
  font-size: 12px;
  opacity: 0.62;
}

.docs-footer-links {
  display: flex;
  gap: 8px;
}
</style>
