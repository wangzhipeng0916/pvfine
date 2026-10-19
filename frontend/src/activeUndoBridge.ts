/**
 * 全局统一撤销/重做桥接中心
 *
 * 背景与根因：
 * 在 Linux (GTK3 CGO) 平台下，Wails v3 运行时底层强制拦截了所有 Ctrl+Z 与 Ctrl+Shift+Z 按键，
 * 阻止其到达 WebKitGTK 的 WebView，并在 Go 侧执行 w.execJS("document.execCommand('undo')")。
 * 现代富文本与代码编辑器（如 CodeMirror 6）采用自主不可变状态树，不依赖浏览器原生的 document.execCommand。
 * 因此在 Linux 桌面应用中，必须 hook document.execCommand 以及监听 beforeinput 事件，
 * 将 Wails 底层发起的撤销/重做指令精确分发给当前处于活跃状态的代码编辑器（如归档对比双栏工作台或主编辑器）。
 *
 * @author flobit
 */

export interface UndoRedoHandler {
  undo: () => boolean;
  redo: () => boolean;
  /** 优先级，数值越高越先尝试响应 */
  priority?: number;
  /** 可选的主动活跃判定 */
  isActive?: () => boolean;
}

const registeredHandlers: UndoRedoHandler[] = [];
let isBridgeInstalled = false;

/**
 * 注册当前界面的撤销/重做处理器。
 * 返回用于注销的清理函数。
 */
export function registerUndoHandler(handler: UndoRedoHandler): () => void {
  registeredHandlers.push(handler);
  registeredHandlers.sort((a, b) => (b.priority ?? 0) - (a.priority ?? 0));
  return () => {
    const idx = registeredHandlers.indexOf(handler);
    if (idx !== -1) {
      registeredHandlers.splice(idx, 1);
    }
  };
}

/**
 * 触发当前活跃处理器的撤销操作
 */
export function triggerActiveUndo(): boolean {
  for (const handler of registeredHandlers) {
    if (handler.isActive && !handler.isActive()) continue;
    try {
      if (handler.undo()) {
        return true;
      }
    } catch (err) {
      console.warn("[activeUndoBridge] 执行撤销失败:", err);
    }
  }
  return false;
}

/**
 * 触发当前活跃处理器的重做操作
 */
export function triggerActiveRedo(): boolean {
  for (const handler of registeredHandlers) {
    if (handler.isActive && !handler.isActive()) continue;
    try {
      if (handler.redo()) {
        return true;
      }
    } catch (err) {
      console.warn("[activeUndoBridge] 执行重做失败:", err);
    }
  }
  return false;
}

function hookDocumentExecCommand(): void {
  if (typeof document === "undefined") return;
  const originalExecCommand = document.execCommand;
  if (typeof originalExecCommand !== "function") return;

  document.execCommand = function (commandId: string, showUI?: boolean, value?: string): boolean {
    const lowerCmd = (commandId || "").toLowerCase();
    if (lowerCmd === "undo") {
      if (triggerActiveUndo()) {
        return true;
      }
    } else if (lowerCmd === "redo") {
      if (triggerActiveRedo()) {
        return true;
      }
    }
    try {
      return originalExecCommand.call(this, commandId, showUI, value);
    } catch {
      return false;
    }
  };
}

function handleGlobalBeforeInput(e: Event): void {
  if (e.defaultPrevented) return;
  const inputEvent = e as InputEvent;
  if (inputEvent.inputType === "historyUndo") {
    if (triggerActiveUndo()) {
      inputEvent.preventDefault();
      inputEvent.stopPropagation();
    }
  } else if (inputEvent.inputType === "historyRedo") {
    if (triggerActiveRedo()) {
      inputEvent.preventDefault();
      inputEvent.stopPropagation();
    }
  }
}

/**
 * 安装全局撤销/重做桥接（幂等，仅需安装一次）
 */
export function installActiveUndoBridge(): void {
  if (isBridgeInstalled || typeof window === "undefined") return;
  isBridgeInstalled = true;

  hookDocumentExecCommand();
  window.addEventListener("beforeinput", handleGlobalBeforeInput, true);
  document.addEventListener("beforeinput", handleGlobalBeforeInput, true);
}
