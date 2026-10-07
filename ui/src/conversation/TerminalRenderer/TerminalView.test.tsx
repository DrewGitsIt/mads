import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { Terminal } from "@xterm/xterm";
import { TerminalView, type TerminalViewHandle } from "./TerminalView";
import type { PtyBridge, TerminalMenuAction } from "./ptyBridge";
import { setPlatform } from "../../platform/shortcut";

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

  it("captures the compose chord (Cmd+E on mac) before xterm — opens compose, writes nothing to the pty", async () => {
    setPlatform("darwin");
    try {
      const { bridge } = makeStubBridge();
      const onComposeOpen = vi.fn();
      const { container } = render(<TerminalView bridge={bridge} onComposeOpen={onComposeOpen} />);
      await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

      const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
      textarea.focus();
      fireEvent.keyDown(textarea, { key: "e", code: "KeyE", metaKey: true });

      expect(onComposeOpen).toHaveBeenCalledTimes(1);
      expect(bridge.write).not.toHaveBeenCalled();
    } finally {
      setPlatform(undefined);
    }
  });

  it("on Linux, Ctrl+Shift+E opens compose and plain Ctrl+E is not intercepted", async () => {
    setPlatform("linux");
    try {
      const { bridge } = makeStubBridge();
      const onComposeOpen = vi.fn();
      const { container } = render(<TerminalView bridge={bridge} onComposeOpen={onComposeOpen} />);
      await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

      const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
      textarea.focus();
      fireEvent.keyDown(textarea, { key: "E", code: "KeyE", keyCode: 69, ctrlKey: true, shiftKey: true });
      expect(onComposeOpen).toHaveBeenCalledTimes(1);
      expect(bridge.write).not.toHaveBeenCalled();

      fireEvent.keyDown(textarea, { key: "e", code: "KeyE", keyCode: 69, which: 69, ctrlKey: true });
      expect(onComposeOpen).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "\u0005"));
    } finally {
      setPlatform(undefined);
    }
  });

  it("without onComposeOpen (docked Shell), plain Ctrl+E reaches the pty as readline end-of-line", async () => {
    setPlatform("linux");
    try {
      const { bridge } = makeStubBridge();
      const { container } = render(<TerminalView bridge={bridge} />);
      await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

      const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
      textarea.focus();
      fireEvent.keyDown(textarea, { key: "e", code: "KeyE", keyCode: 69, which: 69, ctrlKey: true });

      await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "\u0005"));
    } finally {
      setPlatform(undefined);
    }
  });

  it("onToggleView is intercepted only when provided", async () => {
    setPlatform("linux");
    try {
      const { bridge } = makeStubBridge();
      const onToggleView = vi.fn();
      const { container, rerender } = render(<TerminalView bridge={bridge} onToggleView={onToggleView} />);
      await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

      const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
      textarea.focus();
      const chord = { key: "T", code: "KeyT", keyCode: 84, which: 84, ctrlKey: true, shiftKey: true };
      fireEvent.keyDown(textarea, chord);
      expect(onToggleView).toHaveBeenCalledTimes(1);
      expect(bridge.write).not.toHaveBeenCalled();

      rerender(<TerminalView bridge={bridge} />);
      fireEvent.keyDown(textarea, chord);
      expect(onToggleView).toHaveBeenCalledTimes(1);
    } finally {
      setPlatform(undefined);
    }
  });

  it("Ctrl+` writes nothing to the pty and bubbles to the document (so it can hide the shell dock)", async () => {
    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));
    const onDocumentKey = vi.fn();
    document.addEventListener("keydown", onDocumentKey);

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "`", code: "Backquote", keyCode: 192, ctrlKey: true });
    document.removeEventListener("keydown", onDocumentKey);

    expect(onDocumentKey).toHaveBeenCalledTimes(1);
    expect(onDocumentKey.mock.calls[0][0].ctrlKey).toBe(true);
    expect(bridge.write).not.toHaveBeenCalled();
  });

  it("reports a process exit through onExit after printing the exit line", async () => {
    const { bridge, exitListeners } = makeStubBridge();
    const onExit = vi.fn();
    render(<TerminalView bridge={bridge} onExit={onExit} />);
    await waitFor(() => expect(exitListeners.get("pty-1")?.size).toBe(1));

    exitListeners.get("pty-1")!.forEach((l) => l(3));

    expect(onExit).toHaveBeenCalledWith(3);
  });

  it("lets other keys reach the pty as usual — only the compose chord is captured", async () => {
    const { bridge } = makeStubBridge();
    const onComposeOpen = vi.fn();
    const { container } = render(<TerminalView bridge={bridge} onComposeOpen={onComposeOpen} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    textarea.value = "a";
    textarea.dispatchEvent(
      new InputEvent("input", { bubbles: true, data: "a", inputType: "insertText" }),
    );

    await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "a"));
    expect(onComposeOpen).not.toHaveBeenCalled();
  });

  it("exposes an imperative focus() that returns focus to xterm's hidden input", async () => {
    const { bridge } = makeStubBridge();
    const ref = createRef<TerminalViewHandle>();
    const { container } = render(<TerminalView bridge={bridge} ref={ref} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.blur();
    ref.current?.focus();

    expect(document.activeElement).toBe(textarea);
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

describe("TerminalView (copy/paste)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete (window as { mads?: unknown }).mads;
  });

  it("plain Ctrl+C still sends SIGINT down the pty (ETX) rather than copying", async () => {
    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "c", code: "KeyC", keyCode: 67, which: 67, ctrlKey: true });

    await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "\u0003"));
  });

  it("Ctrl+Shift+C copies the selection via the Electron clipboard bridge and writes nothing to the pty", async () => {
    vi.spyOn(Terminal.prototype, "getSelection").mockReturnValue("hello world");
    const writeText = vi.fn().mockResolvedValue(undefined);
    (window as { mads?: unknown }).mads = { clipboard: { writeText, readText: vi.fn() } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "C", code: "KeyC", ctrlKey: true, shiftKey: true });

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("hello world"));
    expect(bridge.write).not.toHaveBeenCalled();
  });

  it("Cmd+C (no shift) also copies on macOS, via the same bridge", async () => {
    vi.spyOn(Terminal.prototype, "getSelection").mockReturnValue("hello world");
    const writeText = vi.fn().mockResolvedValue(undefined);
    (window as { mads?: unknown }).mads = { clipboard: { writeText, readText: vi.fn() } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "c", code: "KeyC", metaKey: true });

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("hello world"));
  });

  it("a Ctrl+Shift+C with no selection is a no-op — never clobbers the clipboard with an empty string", async () => {
    vi.spyOn(Terminal.prototype, "getSelection").mockReturnValue("");
    const writeText = vi.fn();
    (window as { mads?: unknown }).mads = { clipboard: { writeText, readText: vi.fn() } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "C", code: "KeyC", ctrlKey: true, shiftKey: true });

    expect(writeText).not.toHaveBeenCalled();
  });

  it("Ctrl+Shift+V pastes clipboard text into the pty via the Electron bridge", async () => {
    const readText = vi.fn().mockResolvedValue("pasted text");
    (window as { mads?: unknown }).mads = { clipboard: { writeText: vi.fn(), readText } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "V", code: "KeyV", ctrlKey: true, shiftKey: true });

    await waitFor(() => expect(bridge.write).toHaveBeenCalledWith("pty-1", "pasted text"));
  });

  it("falls back to navigator.clipboard outside Electron (no window.mads)", async () => {
    vi.spyOn(Terminal.prototype, "getSelection").mockReturnValue("hello world");
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText, readText: vi.fn().mockResolvedValue("") },
      configurable: true,
    });

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const textarea = container.querySelector(".xterm-helper-textarea") as HTMLTextAreaElement;
    textarea.focus();
    fireEvent.keyDown(textarea, { key: "C", code: "KeyC", ctrlKey: true, shiftKey: true });

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("hello world"));
  });
});

describe("TerminalView (context menu)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete (window as { mads?: unknown }).mads;
  });

  it("has no custom context menu when window.mads is absent — leaves the platform default alone", async () => {
    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const terminal = container.querySelector('[data-testid="terminal-view"]') as HTMLElement;
    const notPrevented = fireEvent.contextMenu(terminal);

    expect(notPrevented).toBe(true);
  });

  it("asks main for a native menu with hasSelection, and performs the returned action", async () => {
    vi.spyOn(Terminal.prototype, "getSelection").mockReturnValue("hello world");
    vi.spyOn(Terminal.prototype, "hasSelection").mockReturnValue(true);
    const writeText = vi.fn().mockResolvedValue(undefined);
    const showTerminal = vi.fn<(state: { hasSelection: boolean }) => Promise<TerminalMenuAction | null>>(
      () => Promise.resolve("copy"),
    );
    (window as { mads?: unknown }).mads = {
      contextMenu: { showTerminal },
      clipboard: { writeText, readText: vi.fn() },
    };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const terminal = container.querySelector('[data-testid="terminal-view"]') as HTMLElement;
    const notPrevented = fireEvent.contextMenu(terminal);

    expect(notPrevented).toBe(false);
    expect(showTerminal).toHaveBeenCalledWith({ hasSelection: true });
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("hello world"));
  });

  it("performs 'clear' when that's the action main resolves with", async () => {
    const clear = vi.spyOn(Terminal.prototype, "clear").mockImplementation(() => {});
    const showTerminal = vi.fn<(state: { hasSelection: boolean }) => Promise<TerminalMenuAction | null>>(
      () => Promise.resolve("clear"),
    );
    (window as { mads?: unknown }).mads = { contextMenu: { showTerminal } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const terminal = container.querySelector('[data-testid="terminal-view"]') as HTMLElement;
    fireEvent.contextMenu(terminal);

    await waitFor(() => expect(clear).toHaveBeenCalled());
  });

  it("does nothing when the menu is dismissed without an action (resolves null)", async () => {
    const showTerminal = vi.fn<(state: { hasSelection: boolean }) => Promise<TerminalMenuAction | null>>(
      () => Promise.resolve(null),
    );
    (window as { mads?: unknown }).mads = { contextMenu: { showTerminal } };

    const { bridge } = makeStubBridge();
    const { container } = render(<TerminalView bridge={bridge} />);
    await waitFor(() => expect(bridge.spawn).toHaveBeenCalledTimes(1));

    const terminal = container.querySelector('[data-testid="terminal-view"]') as HTMLElement;
    fireEvent.contextMenu(terminal);

    await waitFor(() => expect(showTerminal).toHaveBeenCalled());
    expect(bridge.write).not.toHaveBeenCalled();
  });
});
