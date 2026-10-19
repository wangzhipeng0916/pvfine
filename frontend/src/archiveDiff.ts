import type { ArchiveDiffFile } from "../bindings/pvfine/services/models";

export function diffOperation(operation: string, reverse: boolean): string {
  if (!reverse) return operation;
  return operation === "add" ? "delete" : operation === "delete" ? "add" : operation;
}
export function orientArchiveDiff(file: ArchiveDiffFile | null, reverse: boolean): ArchiveDiffFile | null {
  if (!file || !reverse) return file;
  return {
    ...file,
    operation: diffOperation(file.operation, true),
    beforeHash: file.afterHash, afterHash: file.beforeHash,
    beforeType: file.afterType, afterType: file.beforeType,
    beforeSize: file.afterSize, afterSize: file.beforeSize,
    beforeExists: file.afterExists, afterExists: file.beforeExists,
    beforeText: file.afterText, afterText: file.beforeText,
  };
}
export function archiveName(path: string): string {
  return path.split(/[\\/]/).pop() || "当前工作区";
}

/**
 * 判断当前活动焦点是否位于外部独立输入控件中（如搜索框、普通输入文本框等）
 * 代码编辑器内部的 contenteditable 和 CodeMirror 内部容器不被视作外部输入框
 * @author flobit
 */
export function isExternalInputElement(el: Element | null, editorContainer?: Element | null): boolean {
  if (!el) return false;
  if (editorContainer && el.closest(".cm-editor") && !editorContainer.contains(el)) return true;
  const isInput = el.tagName === "INPUT" || el.tagName === "TEXTAREA" || (el as HTMLElement).isContentEditable;
  if (!isInput) return false;
  if (editorContainer && editorContainer.contains(el)) return false;
  if (el.closest(".cm-editor")) return false;
  return true;
}

export function isArchiveDiffShortcutActive(active: boolean, el: Element | null, container: Element | null): boolean {
  if (!active || !container) return false;
  if (el?.closest('[role="dialog"], [role="alertdialog"]') && !container.contains(el)) return false;
  return !isExternalInputElement(el, container);
}
