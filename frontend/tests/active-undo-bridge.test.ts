import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  installActiveUndoBridge,
  registerUndoHandler,
  triggerActiveUndo,
  triggerActiveRedo,
} from "../src/activeUndoBridge";

describe("activeUndoBridge", () => {
  const listeners: Record<string, Function[]> = {};

  beforeEach(() => {
    // 确保 mock document/window 环境
    if (typeof document === "undefined") {
      (global as any).document = {
        execCommand: vi.fn(),
        addEventListener: vi.fn((type: string, fn: any) => {
          listeners[type] = listeners[type] || [];
          listeners[type].push(fn);
        }),
        removeEventListener: vi.fn(),
      };
      (global as any).window = {
        addEventListener: vi.fn((type: string, fn: any) => {
          listeners[type] = listeners[type] || [];
          listeners[type].push(fn);
        }),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn((evt: any) => {
          const fns = listeners[evt.type] || [];
          for (const fn of fns) fn(evt);
          return !evt.defaultPrevented;
        }),
      };
    } else {
      if (!(window as any).dispatchEvent) {
        (window as any).dispatchEvent = vi.fn((evt: any) => {
          const fns = listeners[evt.type] || [];
          for (const fn of fns) fn(evt);
          return !evt.defaultPrevented;
        });
      }
    }
  });

  it("应按优先级顺序调用活跃处理器的 undo 和 redo", () => {
    const handlerLowUndo = vi.fn().mockReturnValue(false);
    const handlerHighUndo = vi.fn().mockReturnValue(true);
    const handlerHighRedo = vi.fn().mockReturnValue(true);

    const unreg1 = registerUndoHandler({
      undo: handlerLowUndo,
      redo: () => false,
      priority: 10,
    });

    const unreg2 = registerUndoHandler({
      undo: handlerHighUndo,
      redo: handlerHighRedo,
      priority: 100,
    });

    // 触发 undo：应该优先调用 priority 100 的 handler
    const res = triggerActiveUndo();
    expect(res).toBe(true);
    expect(handlerHighUndo).toHaveBeenCalledTimes(1);
    expect(handlerLowUndo).not.toHaveBeenCalled();

    // 触发 redo
    const resRedo = triggerActiveRedo();
    expect(resRedo).toBe(true);
    expect(handlerHighRedo).toHaveBeenCalledTimes(1);

    // 注销高优先级 handler
    unreg2();
    handlerLowUndo.mockReturnValue(true);

    const res2 = triggerActiveUndo();
    expect(res2).toBe(true);
    expect(handlerLowUndo).toHaveBeenCalledTimes(1);

    unreg1();
  });

  it("hook document.execCommand 能够成功拦截并调用 triggerActiveUndo", () => {
    installActiveUndoBridge();

    const undoFn = vi.fn().mockReturnValue(true);
    const unreg = registerUndoHandler({
      undo: undoFn,
      redo: () => false,
      priority: 50,
    });

    const execRes = document.execCommand("undo");
    expect(execRes).toBe(true);
    expect(undoFn).toHaveBeenCalledTimes(1);

    unreg();
  });

  it("当高优先级处理器 isActive 为 false 时，应跳过并顺延至下一个活跃处理器", () => {
    const inactiveUndo = vi.fn().mockReturnValue(true);
    const activeUndo = vi.fn().mockReturnValue(true);

    const unreg1 = registerUndoHandler({
      undo: inactiveUndo,
      redo: () => false,
      priority: 100,
      isActive: () => false,
    });

    const unreg2 = registerUndoHandler({
      undo: activeUndo,
      redo: () => false,
      priority: 50,
      isActive: () => true,
    });

    const res = triggerActiveUndo();
    expect(res).toBe(true);
    expect(inactiveUndo).not.toHaveBeenCalled();
    expect(activeUndo).toHaveBeenCalledTimes(1);

    unreg1();
    unreg2();
  });

  it("当处理器执行抛出异常时，应安全捕获并继续尝试后续处理器", () => {
    const errorUndo = vi.fn().mockImplementation(() => {
      throw new Error("test crash");
    });
    const fallbackUndo = vi.fn().mockReturnValue(true);

    const unreg1 = registerUndoHandler({
      undo: errorUndo,
      redo: () => false,
      priority: 100,
    });

    const unreg2 = registerUndoHandler({
      undo: fallbackUndo,
      redo: () => false,
      priority: 50,
    });

    const res = triggerActiveUndo();
    expect(res).toBe(true);
    expect(errorUndo).toHaveBeenCalledTimes(1);
    expect(fallbackUndo).toHaveBeenCalledTimes(1);

    unreg1();
    unreg2();
  });

  it("beforeinput 事件应能触发撤销并阻止默认事件行为", () => {
    installActiveUndoBridge();

    const undoFn = vi.fn().mockReturnValue(true);
    const unreg = registerUndoHandler({
      undo: undoFn,
      redo: () => false,
      priority: 80,
    });

    const event = new Event("beforeinput", { cancelable: true });
    Object.defineProperty(event, "inputType", { value: "historyUndo" });

    window.dispatchEvent(event);
    expect(undoFn).toHaveBeenCalledTimes(1);
    expect(event.defaultPrevented).toBe(true);

    unreg();
  });
});
