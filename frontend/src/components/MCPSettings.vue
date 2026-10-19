<script setup lang="ts">
import { onMounted, ref } from "vue";
import { NAlert, NButton, NInputNumber, NSwitch, useMessage } from "naive-ui";
import { Clipboard } from "@wailsio/runtime";
import { MCPService } from "../../bindings/pvfine/services";
import type { MCPStatus } from "../../bindings/pvfine/services/models";

const message = useMessage();
const status = ref<MCPStatus | null>(null);
const port = ref(19245);
const loading = ref(false);
async function refresh() {
  try { status.value = await MCPService.Status(); port.value = status.value.port; }
  catch { message.error("无法读取 MCP 状态"); }
}
async function configure(enabled: boolean) {
  loading.value = true;
  try { status.value = await MCPService.Configure(enabled, port.value); }
  catch (error: any) { message.error(String(error?.message ?? error)); await refresh(); }
  finally { loading.value = false; }
}
async function copyConfig() {
  try {
    await Clipboard.SetText(await MCPService.ConnectionConfig());
    message.success("连接配置已复制，请粘贴到 AI 客户端的 MCP 配置中");
  } catch { message.error("复制连接配置失败"); }
}
onMounted(refresh);
</script>

<template>
  <section class="mcp-settings">
    <h3>MCP 服务</h3>
    <p>让 AI 连接当前 pvfine，搜索和编辑脚本、管理归档、导入文件，以及导出普通文件或 MOD。</p>
    <div class="mcp-row">
      <span>启用本机服务</span>
      <NSwitch :value="status?.enabled ?? false" :loading="loading" :disabled="loading || status?.busy" @update:value="configure" />
    </div>
    <div class="mcp-row">
      <span>端口</span>
      <NInputNumber v-model:value="port" :min="1024" :max="65535" :disabled="loading || status?.enabled" style="width: 140px" />
    </div>
    <NAlert v-if="status?.error" type="error">{{ status.error }}</NAlert>
    <template v-if="status?.enabled">
      <p class="mcp-endpoint">{{ status.endpoint }}</p>
      <NButton type="primary" secondary @click="copyConfig">复制连接配置</NButton>
      <p>连接配置仅包含本机服务地址，无需鉴权。端口不变时，重启后可继续使用原配置。</p>
    </template>
    <p>AI 的修改先进入预览，应用后再保存 PVF。另存为仅生成副本，后续保存仍写原文件。归档编辑器有未提交草稿时，AI 操作会等待你先处理草稿。</p>
  </section>
</template>

<style scoped>
.mcp-settings { max-width: 760px; padding: 8px 0; }
.mcp-settings h3 { font-size: 20px; margin: 0 0 16px; }
.mcp-settings p { color: var(--pvf-text-secondary); line-height: 1.7; }
.mcp-row { display: flex; justify-content: space-between; align-items: center; padding: 18px 0; border-bottom: 1px solid var(--pvf-border); }
.mcp-endpoint { font-family: monospace; user-select: text; }
</style>
