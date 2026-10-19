import { beforeEach, expect, test, vi } from "vitest";
import { hasArchiveChanges, hasUnsavedWorkspaceChanges } from "../src/unsaved";

const state = vi.hoisted(() => ({
  archive: { modifiedCount: 0 },
  editor: { dirtyCount: 0 },
  fileSets: { dirty: false },
  script: { dirty: false, workspaceDetached: false, detachedDirty: false },
  version: { status: { changedFiles: 0, needsSave: false } },
}));
vi.mock("../src/stores/archive", () => ({ useArchiveStore: () => state.archive }));
vi.mock("../src/stores/editor", () => ({ useEditorStore: () => state.editor }));
vi.mock("../src/stores/fileSets", () => ({ useFileSetStore: () => state.fileSets }));
vi.mock("../src/stores/script", () => ({ useScriptStore: () => state.script }));
vi.mock("../src/stores/version", () => ({ useVersionStore: () => state.version }));

beforeEach(() => { state.editor.dirtyCount = 0; });

test("editor dirty count protects unsaved drafts at workspace exit and archive backup", () => {
  expect(hasUnsavedWorkspaceChanges()).toBe(false);
  expect(hasArchiveChanges()).toBe(false);
  state.editor.dirtyCount = 1;
  expect(hasUnsavedWorkspaceChanges()).toBe(true);
  expect(hasArchiveChanges()).toBe(true);
});
