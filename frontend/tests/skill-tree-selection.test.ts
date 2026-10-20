import { beforeEach, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, toRef } from "vue";
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn() } }));
import { useFileGUIStore } from "../src/stores/fileGUI";

beforeEach(() => setActivePinia(createPinia()));
it("弹窗组件销毁后，重新创建的选择引用仍恢复职业、转职和SP/TP", () => {
  const first = effectScope();
  first.run(() => {
    const gui = useFileGUIStore();
    toRef(gui.skillTreeSelection, "job").value = "[swordman]";
    toRef(gui.skillTreeSelection, "branch").value = "weaponmaster";
    toRef(gui.skillTreeSelection, "kind").value = "tp";
  });
  first.stop();
  const reopened = effectScope();
  reopened.run(() => {
    expect(useFileGUIStore().skillTreeSelection).toEqual({job: "[swordman]", branch: "weaponmaster", kind: "tp"});
  });
  reopened.stop();
});
