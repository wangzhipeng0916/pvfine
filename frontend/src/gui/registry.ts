import { defineAsyncComponent } from "vue";
import type { GUIFile, GUIProvider } from "./types";

function normalizePath(path: string): string {
  return path.replaceAll("\\", "/").replace(/^\/+|\/+$/g, "").toLowerCase();
}

const providers: GUIProvider[] = [
  {
    id: "shop",
    label: "商店",
    readOnly: false,
    matches: (file) => file.path.toLowerCase().endsWith(".shp"),
    component: defineAsyncComponent(() => import("../components/gui/ShopViewer.vue")),
  },
  {
    id: "world-drop",
    label: "全局掉率",
    readOnly: false,
    matches: (file) => ["etc/worlddrop.etc", "etc/(r)worlddrop.etc"].includes(normalizePath(file.path)),
    component: defineAsyncComponent(() => import("../components/gui/WorldDropViewer.vue")),
  },
  {
    id: "skill",
    label: "技能",
    readOnly: false,
    matches: (file) => file.path.toLowerCase().endsWith(".skl"),
    component: defineAsyncComponent(() => import("../components/gui/SkillViewer.vue")),
  },
  {
    id: "quest",
    label: "任务",
    readOnly: false,
    matches: (file) => file.path.toLowerCase().endsWith(".qst"),
    component: defineAsyncComponent(() => import("../components/gui/QuestViewer.vue")),
  },
  {
    id: "upgrade",
    label: "强化/增幅",
    readOnly: false,
    matches: (file) => ["etc/upgrade.etc", "etc/amplifyupgrade.etc"].includes(normalizePath(file.path)),
    component: defineAsyncComponent(() => import("../components/gui/UpgradeViewer.vue")),
  },
  {
    id: "forge",
    label: "锻造",
    readOnly: false,
    matches: (file) => normalizePath(file.path) === "etc/upgrade_separate.etc",
    component: defineAsyncComponent(() => import("../components/gui/ForgeViewer.vue")),
  },
  {
    id: "item",
    label: "物品",
    readOnly: false,
    matches: (file) => /\.(equ|stk)$/i.test(file.path),
    component: defineAsyncComponent(() => import("../components/gui/ItemViewer.vue")),
  },
];

export function getGUIProvider(file: GUIFile): GUIProvider | null {
  return providers.find((provider) => provider.matches(file)) ?? null;
}
