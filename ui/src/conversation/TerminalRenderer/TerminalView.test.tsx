import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { TerminalView } from "./TerminalView";
import type { PtyBridge } from "./ptyBridge";

/** In-memory PtyBridge stub — no Electron, no IPC, no node-pty. */
function makeStubBridge() {
  const dataListeners = new Map<string, Set<(data: string) => void>>();
  const exitListeners = new Map<string, Set<(code: number) => void>>();
  const bridge: PtyBridge = {
    spawn: vi.fn(async () => "pty-1"),
    write: vi.fn(),
    resize: vi.fn(),
    kill: vi.fn(),
    onData: vi.fn((id, listener) => {
      const set = dataListeners.get(id) ?? new Set();
      set.add(listener);
      dataListeners.set(id, set);
      return () => set.delete(listener);
    }),
    onExit: vi.fn((id, listener) => {
      const set = exitListeners.get(id) ?? new Set();
      set.add(listener);
      exitListeners.set(id, set);
      return () => set.delete(listener);
    }),
  };
  return { bridge, dataListeners, exitListeners };
}

describe("TerminalView (renderer contract)", () => {
  it("mounts and spawns a pty via the bridge", async () => {
    const { bridge } = makeStubBridge();
    render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));
  });

  it("writes to the bridge on user input", async () => {
    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    expect(textarea).toBeTruthy();
    textarea.focus();
    textarea.value = textarea.value + "a";
    textarea.dispatchEvent(
      new InputEvent("input", { bubbles: true, data: "a", inputType: "insertText" }),
    );

    await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "a"));
  });

  it("calls resize on the bridge when the container resizes", async () => {
    const resizeCallbacks: Array<() => void> = [];
    const OriginalResizeObserver = globalThis.ResizeObserver;
    globalThis.ResizeObserver = class {
      constructor(cb: () => void) {
        resizeCallbacks.push(cb);
      }
      observe() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;

    const { bridge } = makeStubBridge();
    render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    resizeCallbacks.forEach((cb) => cb());
    expect(bridge.resize).toHaveBeenCalled();

    globalThis.ResizeObserver = OriginalResizeObserver;
  });

  it("calls kill and unsubscribes on unmount", async () => {
    const { bridge } = makeStubBridge();
    const { unmount } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    unmount();
    expect(bridge.kill).toHaveBeenCalledWith("pty-1");
  });

  // Regression test: a mounted TerminalView must re-subscribe when its
  // bridge prop's identity changes (e.g. ConversationTab reconnecting its
  // SessionEventSource and rebuilding SessionTerminalBridge around it)
  // instead of going on listening to the old, now-disconnected bridge
  // forever — the root cause of the reported "frozen terminal" bug, which
  // previously required a full remount (navigate away and back) to clear.
  it("re-subscribes to a new bridge when the bridge prop's identity changes, without unmounting", async () => {
    const stub1 = makeStubBridge();
    const stub2 = makeStubBridge();
    const { rerender } = render(<TerminalView bridge={stub1.bridge} />);
    await waitFor(() => expect(stub1.bridge.spawn).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(stub1.bridge.onData).toHaveBeenCalledTimes(1));

    rerender(<TerminalView bridge={stub2.bridge} />);

    await waitFor(() => expect(stub2.bridge.spawn).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(stub2.bridge.onData).toHaveBeenCalledTimes(1));
    // The old bridge's data listener must be gone — otherwise live bytes
    // from the new bridge and stale reads from the old one could both fire.
    expect(stub1.dataListeners.get("pty-1")?.size ?? 0).toBe(0);
    expect(stub2.dataListeners.get("pty-1")?.size ?? 0).toBe(1);
  });
});
