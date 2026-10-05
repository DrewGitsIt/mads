import { contextBridge, ipcRenderer } from "electron";

interface PtySpawnOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
  shell?: string;
}

/**
 * Renderer-facing pty namespace — mirrors the PtyBridge interface in
 * ui/src/conversation/TerminalRenderer/ptyBridge.ts. Wraps ipcRenderer so no
 * raw ipcRenderer/node-pty ever crosses into the renderer (contextIsolation
 * stays on in main.ts).
 */
const pty = {
  spawn: (options?: PtySpawnOptions): Promise<string> =>
    ipcRenderer.invoke("pty:spawn", options),
  write: (id: string, data: string): void => {
    void ipcRenderer.invoke("pty:write", { id, data });
  },
  resize: (id: string, cols: number, rows: number): void => {
    void ipcRenderer.invoke("pty:resize", { id, cols, rows });
  },
  kill: (id: string): void => {
    void ipcRenderer.invoke("pty:kill", { id });
  },
  onData: (id: string, listener: (data: string) => void): (() => void) => {
    const handler = (_event: unknown, payload: { id: string; data: string }) => {
      if (payload.id === id) listener(payload.data);
    };
    ipcRenderer.on("pty:data", handler);
    return () => ipcRenderer.removeListener("pty:data", handler);
  },
  onExit: (id: string, listener: (exitCode: number) => void): (() => void) => {
    const handler = (_event: unknown, payload: { id: string; exitCode: number }) => {
      if (payload.id === id) listener(payload.exitCode);
    };
    ipcRenderer.on("pty:exit", handler);
    return () => ipcRenderer.removeListener("pty:exit", handler);
  },
};

/**
 * Renderer-facing badge namespace — mirrors the BadgeBridge interface in
 * ui/src/features/command-palette/badgeBridge.ts.
 */
const badge = {
  setCount: (count: number): void => {
    void ipcRenderer.invoke("badge:set-count", count);
  },
};

/**
 * Renderer-facing dialog namespace — mirrors the DirectoryPicker interface in
 * ui/src/platform/directoryPicker.ts. Resolves null when the user cancels.
 */
const dialog = {
  pickDirectory: (options?: { title?: string; defaultPath?: string }): Promise<string | null> =>
    ipcRenderer.invoke("dialog:pick-directory", options),
};

type TerminalMenuAction = "copy" | "paste" | "selectAll" | "clear";

/**
 * Renderer-facing context-menu namespace — mirrors the TerminalContextMenu
 * interface in ui/src/conversation/TerminalRenderer/ptyBridge.ts. Main pops
 * the native menu and resolves which item (if any) was clicked; the renderer
 * carries out the action itself since only it holds the Terminal instance.
 */
const contextMenu = {
  showTerminal: (state: { hasSelection: boolean }): Promise<TerminalMenuAction | null> =>
    ipcRenderer.invoke("contextMenu:terminal", state),
};

/**
 * Renderer-facing clipboard namespace — mirrors the ClipboardBridge interface
 * in ptyBridge.ts. Routed through main rather than calling the `clipboard`
 * module here directly: this window's BrowserWindow uses Electron's default
 * `sandbox: true`, and a sandboxed preload's restricted `require('electron')`
 * doesn't include `clipboard` (only contextBridge/ipcRenderer/a handful of
 * others) — main is unsandboxed and always has it.
 */
const clipboard = {
  readText: (): Promise<string> => ipcRenderer.invoke("clipboard:read-text"),
  writeText: (text: string): Promise<void> => ipcRenderer.invoke("clipboard:write-text", text),
};

interface ShellOpenOptions {
  cwd?: string;
  cols?: number;
  rows?: number;
}

interface ShellOpenResult {
  shellId: string;
  created: boolean;
  /** The session's last shell exited on its own while detached. */
  previousExit?: { exitCode: number; exitedAt: number };
  /**
   * Only when created is false: the live shell's screen, to write into a
   * fresh terminal of the open's size before any `data` arrives.
   */
  snapshot?: string;
}

interface ShellInfo {
  sessionId: string;
  shellId: string;
  cwd: string | undefined;
  pid: number | undefined;
  startedAt: number;
  attached: boolean;
}

type ShellEventMessage =
  | { event: "data"; shellId: string; data: string }
  | { event: "exit"; shellId: string; exitCode: number }
  | { event: "detached"; shellId: string };

type ShellReply = { id: number; ok: true; result: unknown } | { id: number; ok: false; error: string };

/**
 * Renderer-facing shell namespace — mirrors the ShellHostBridge interface in
 * ptyBridge.ts. Talks to the shell host utilityProcess (see shellHost.ts)
 * over a MessagePort that main brokers on request; the port itself never
 * crosses contextBridge, only these narrow functions do. The port is fetched
 * lazily on first use, which also means a reloaded page (a fresh preload)
 * re-brokers on its own, and a host crash just clears the port so the next
 * call asks main for a new one (forking a fresh host).
 */
const shellListeners = {
  data: new Map<string, Set<(data: string) => void>>(),
  exit: new Map<string, Set<(exitCode: number) => void>>(),
  detached: new Map<string, Set<() => void>>(),
  hostExited: new Set<() => void>(),
};
const shellPending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
let shellPort: Promise<MessagePort> | undefined;
let shellRequestId = 0;

function dispatchShellMessage(message: ShellEventMessage | ShellReply): void {
  if ("event" in message) {
    if (message.event === "data") {
      shellListeners.data.get(message.shellId)?.forEach((listener) => listener(message.data));
    } else if (message.event === "exit") {
      shellListeners.exit.get(message.shellId)?.forEach((listener) => listener(message.exitCode));
    } else {
      shellListeners.detached.get(message.shellId)?.forEach((listener) => listener());
    }
    return;
  }
  const pending = shellPending.get(message.id);
  if (!pending) return;
  shellPending.delete(message.id);
  if (message.ok) pending.resolve(message.result);
  else pending.reject(new Error(message.error));
}

function connectShellPort(): Promise<MessagePort> {
  if (!shellPort) {
    const attempt = new Promise<MessagePort>((resolve, reject) => {
      // Listen before asking, so the port can't arrive unseen.
      ipcRenderer.once("shell:port", (event) => {
        const port = event.ports[0];
        port.onmessage = (e: MessageEvent) =>
          dispatchShellMessage(e.data as ShellEventMessage | ShellReply);
        resolve(port);
      });
      ipcRenderer.invoke("shell:connect").catch(reject);
    });
    attempt.catch(() => {
      if (shellPort === attempt) shellPort = undefined;
    });
    shellPort = attempt;
  }
  return shellPort;
}

ipcRenderer.on("shell:host-exited", () => {
  const closing = shellPort;
  shellPort = undefined;
  void closing?.then((port) => port.close(), () => undefined);
  for (const pending of shellPending.values()) pending.reject(new Error("shell host exited"));
  shellPending.clear();
  shellListeners.hostExited.forEach((listener) => listener());
});

function shellRequest<T>(request: Record<string, unknown>): Promise<T> {
  return connectShellPort().then(
    (port) =>
      new Promise<T>((resolve, reject) => {
        const id = ++shellRequestId;
        shellPending.set(id, { resolve: resolve as (v: unknown) => void, reject });
        port.postMessage({ id, ...request });
      }),
  );
}

/** Fire-and-forget: no id, so the host sends no reply (keystrokes would double the traffic). */
function shellSend(request: Record<string, unknown>): void {
  void connectShellPort().then((port) => port.postMessage(request), () => undefined);
}

function shellSubscribe<T>(
  registry: Map<string, Set<T>>,
  shellId: string,
  listener: T,
): () => void {
  let set = registry.get(shellId);
  if (!set) registry.set(shellId, (set = new Set()));
  set.add(listener);
  return () => {
    set.delete(listener);
    if (set.size === 0) registry.delete(shellId);
  };
}

const shellBridge = {
  open: (sessionId: string, options?: ShellOpenOptions): Promise<ShellOpenResult> =>
    shellRequest({ type: "open", sessionId, ...options }),
  detach: (shellId: string): Promise<void> => shellRequest({ type: "detach", shellId }),
  end: (shellId: string): Promise<void> => shellRequest({ type: "end", shellId }),
  write: (shellId: string, data: string): void => {
    shellSend({ type: "write", shellId, data });
  },
  resize: (shellId: string, cols: number, rows: number): void => {
    shellSend({ type: "resize", shellId, cols, rows });
  },
  list: (): Promise<ShellInfo[]> => shellRequest({ type: "list" }),
  onData: (shellId: string, listener: (data: string) => void): (() => void) =>
    shellSubscribe(shellListeners.data, shellId, listener),
  onExit: (shellId: string, listener: (exitCode: number) => void): (() => void) =>
    shellSubscribe(shellListeners.exit, shellId, listener),
  onDetached: (shellId: string, listener: () => void): (() => void) =>
    shellSubscribe(shellListeners.detached, shellId, listener),
  onHostExited: (listener: () => void): (() => void) => {
    shellListeners.hostExited.add(listener);
    return () => shellListeners.hostExited.delete(listener);
  },
};

contextBridge.exposeInMainWorld("mads", { pty, badge, dialog, contextMenu, clipboard, shell: shellBridge });
