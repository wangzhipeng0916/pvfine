<script setup lang="ts">
import { ref } from "vue";
import { NButton, NIcon, NTabPane, NTabs } from "naive-ui";
import { ArrowUpRight24Regular } from "@vicons/fluent";
import { Browser } from "@wailsio/runtime";
import { extractMarkdownSection, plainMarkdown } from "../docs";
import changelogLocalRaw from "../../../CHANGELOG.fork.md?raw";
import changelogUpstreamRaw from "../../../CHANGELOG.md?raw";
import readmeRaw from "../../../README.md?raw";

const activeTab = ref("features");

// README 的简介与核心特性拼起来才是「功能列表与介绍」：只给列表会丢掉技术栈前提，
// 只给简介又看不到具体能力。
const features = plainMarkdown(
  [
    extractMarkdownSection(readmeRaw, "项目简介"),
    extractMarkdownSection(readmeRaw, "核心特性"),
  ].join("\n\n")
);
const upstreamLog = plainMarkdown(changelogUpstreamRaw);
const localLog = plainMarkdown(changelogLocalRaw);

const versionMarks = (markdown: string) => (markdown.match(/^## \[.*/gm) ?? []).length;

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
  <section class="settings-group">
    <div class="group-header">
      <div class="group-title">产品文档</div>
      <div class="group-subtitle">
        正文在构建时嵌入，不联网、不随远端更新；需要最新记录请先升级
      </div>
    </div>

    <div class="docs-panel">
      <NTabs v-model:value="activeTab" type="line" animated pane-style="padding-top: 10px">
        <NTabPane name="features" tab="功能列表与介绍">
          <pre class="docs-body">{{ features }}</pre>
        </NTabPane>
        <NTabPane name="upstream" :tab="`上游更新日志 · ${versionMarks(changelogUpstreamRaw)} 版`">
          <pre class="docs-body">{{ upstreamLog }}</pre>
        </NTabPane>
        <NTabPane name="local" :tab="`本分支更新日志 · ${versionMarks(changelogLocalRaw)} 条`">
          <pre class="docs-body">{{ localLog }}</pre>
        </NTabPane>
      </NTabs>
      <div class="docs-footer">
        <NButton v-for="link in links" :key="link.url" size="tiny" secondary @click="openLink(link.url)">
          <template #icon><NIcon :size="13"><ArrowUpRight24Regular /></NIcon></template>
          {{ link.label }}
        </NButton>
      </div>
    </div>
  </section>
</template>

<style scoped>
.docs-panel {
  padding: 14px 16px 16px;
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 12px;
  background: var(--pvf-surface-card);
}

.docs-body {
  max-height: 60vh;
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
  gap: 8px;
  margin-top: 12px;
}
</style>
