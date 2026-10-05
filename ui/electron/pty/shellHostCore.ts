import { randomUUID } from "node:crypto";
import { defaultShell, resolveCwd, type NodePtyModule, type PtyProcess } from "./ptyHost";
import { shellEnv } from "./shellEnv";

/**
 * The shell host's message-handling core: one shell per session id, attached
 * to at most one window at a time. It owns no Electron or node-pty import —
 * the pty module and the per-window senders are injected — so it is unit
 * testable with a fake pty (shellHostCore.test.ts) and exercisable against
 * real node-pty without Electron (shellHost.realtest.ts). shellHost.ts is the
 * thin utilityProcess layer that feeds it MessagePorts. The protocol is
 * docs/SHELL-DOCK-DESIGN.md D2.
 *
 * Windows are opaque numeric ids (main tags each port with its webContents
 * id). A shell outlives whichever window is looking at it: `detach` and a
 * window going away only unhook the view; the process keeps running until
 * `end`, its own exit, or the host dying.
 */

export type ShellRequest =
  | { type: "open"; sessionId: string; cwd?: string; cols?: number; rows?: number }
  | { type: "detach"; shellId: string }
  | { type: "end"; shellId: string }
  | { type: "write"; shellId: string; data: string }
  | { type: "resize"; shellId: string; cols: number; rows: number }
  | { type: "list" };

/** Pushed to a window's port. `data`/`exit` only ever reach the attached window. */
export type ShellEvent =
  | { event: "data"; shellId: string; data: string }
  | { event: "exit"; shellId: string; exitCode: number }
  | { event: "detached"; shellId: string };

export interface ShellOpenResult {
  shellId: string;
  /** True when this open started the shell; false when it attached to a live one. */
  created: boolean;
  /**
   * Set when the session's previous shell exited on its own while no window
   * was attached (the user never saw it die). Reported once, by the open that
   * starts the replacement, so the dock can say "[process exited]".
   */
  previousExit?: { exitCode: number; exitedAt: number };
}

export interface ShellInfo {
  sessionId: string;
  shellId: string;
  cwd: string | undefined;
  pid: number | undefined;
  /** Epoch milliseconds. */
  startedAt: number;
  /** Whether some window is currently attached (a hidden shell is not). */
  attached: boolean;
}

export type SendToWindow = (event: ShellEvent) => void;

export interface ShellHostDeps {
  /** Resolves undefined when node-pty is unavailable (see loadNodePty). */
  loadPty: () => Promise<NodePtyModule | undefined>;
  env?: () => NodeJS.ProcessEnv;
  shell?: () => string;
  now?: () => number;
  newId?: () => string;
}

interface ShellEntry {
  sessionId: string;
  shellId: string;
  proc: PtyProcess;
  cwd: string | undefined;
  startedAt: number;
  attachedTo: number | undefined;
}

export class ShellHostCore {
  private readonly bySession = new Map<string, ShellEntry>();
  private readonly byId = new Map<string, ShellEntry>();
  // Detached-exit notices by session, consumed by that session's next open.
  private readonly tombstones = new Map<string, { exitCode: number; exitedAt: number }>();
  private readonly windows = new Map<number, SendToWindow>();

  constructor(private readonly deps: ShellHostDeps) {}

  /** Registers a window's event sink. Reconnecting an id replaces (and detaches) the old one. */
  connect(windowId: number, send: SendToWindow): void {
    this.disconnect(windowId);
    this.windows.set(windowId, send);
  }

  /** The window is gone (reload, crash, close): detach its shells, keep them running. */
  disconnect(windowId: number): void {
    this.windows.delete(windowId);
    for (const entry of this.byId.values()) {
      if (entry.attachedTo === windowId) entry.attachedTo = undefined;
    }
  }

  async handle(windowId: number, request: ShellRequest): Promise<unknown> {
    if (!this.windows.has(windowId)) {
      throw new Error(`window ${windowId} is not connected to the shell host`);
    }
    switch (request.type) {
      case "open":
        return this.open(windowId, request);
      case "detach":
        // Only the attached window can detach; a stale view must not unhook a newer one.
        if (this.owned(windowId, request.shellId)) this.detach(request.shellId);
        return undefined;
      case "end":
        this.end(request.shellId);
        return undefined;
      case "write":
        this.owned(windowId, request.shellId)?.proc.write(request.data);
        return undefined;
      case "resize":
        this.owned(windowId, request.shellId)?.proc.resize(request.cols, request.rows);
        return undefined;
      case "list":
        return this.list();
    }
  }

  list(): ShellInfo[] {
    return [...this.byId.values()].map((entry) => ({
      sessionId: entry.sessionId,
      shellId: entry.shellId,
      cwd: entry.cwd,
      pid: entry.proc.pid,
      startedAt: entry.startedAt,
      attached: entry.attachedTo !== undefined,
    }));
  }

  /** The shell, but only if this window is the one attached — others can't drive it. */
  private owned(windowId: number, shellId: string): ShellEntry | undefined {
    const entry = this.byId.get(shellId);
    return entry && entry.attachedTo === windowId ? entry : undefined;
  }

  private async open(
    windowId: number,
    request: Extract<ShellRequest, { type: "open" }>,
  ): Promise<ShellOpenResult> {
    const nodePty = await this.deps.loadPty();
    if (!nodePty) {
      throw new Error("node-pty is unavailable on this machine");
    }
    // Everything below is synchronous, so two concurrent opens for one session
    // can't both see "no shell" and spawn twice.
    const sink = this.windows.get(windowId);
    if (!sink) throw new Error(`window ${windowId} is not connected to the shell host`);

    const existing = this.bySession.get(request.sessionId);
    if (existing) {
      // Latest open wins: the window that had it learns it lost it.
      const previous = existing.attachedTo;
      existing.attachedTo = windowId;
      if (previous !== undefined && previous !== windowId) {
        this.windows.get(previous)?.({ event: "detached", shellId: existing.shellId });
      }
      // The new view may be a different size than the one that left.
      if (request.cols && request.rows) existing.proc.resize(request.cols, request.rows);
      return { shellId: existing.shellId, created: false };
    }

    const cwd = resolveCwd(request.cwd);
    const proc = nodePty.spawn((this.deps.shell ?? defaultShell)(), [], {
      name: "xterm-256color",
      cols: request.cols ?? 80,
      rows: request.rows ?? 24,
      cwd,
      env: shellEnv((this.deps.env ?? (() => process.env))()),
    });
    const entry: ShellEntry = {
      sessionId: request.sessionId,
      shellId: (this.deps.newId ?? randomUUID)(),
      proc,
      cwd,
      startedAt: (this.deps.now ?? Date.now)(),
      attachedTo: windowId,
    };
    this.bySession.set(entry.sessionId, entry);
    this.byId.set(entry.shellId, entry);

    proc.onData((data) => {
      // Detached output is dropped here; the D3 mirror will catch it later.
      this.sendToAttached(entry, { event: "data", shellId: entry.shellId, data });
    });
    proc.onExit(({ exitCode }) => {
      // An `end` already removed the entry and nobody is waiting on it.
      if (this.byId.get(entry.shellId) !== entry) return;
      if (entry.attachedTo === undefined) {
        // Nobody was watching; remember it so the next open can say so.
        this.tombstones.set(entry.sessionId, { exitCode, exitedAt: (this.deps.now ?? Date.now)() });
      }
      this.sendToAttached(entry, { event: "exit", shellId: entry.shellId, exitCode });
      this.remove(entry);
    });
    const previousExit = this.tombstones.get(request.sessionId);
    this.tombstones.delete(request.sessionId);
    return previousExit
      ? { shellId: entry.shellId, created: true, previousExit }
      : { shellId: entry.shellId, created: true };
  }

  private detach(shellId: string): void {
    const entry = this.byId.get(shellId);
    if (entry) entry.attachedTo = undefined;
  }

  private end(shellId: string): void {
    const entry = this.byId.get(shellId);
    if (!entry) return;
    this.remove(entry);
    entry.proc.kill();
  }

  /** Ends every shell, e.g. when the host is shutting down. */
  endAll(): void {
    for (const entry of [...this.byId.values()]) this.end(entry.shellId);
  }

  private remove(entry: ShellEntry): void {
    this.byId.delete(entry.shellId);
    if (this.bySession.get(entry.sessionId) === entry) this.bySession.delete(entry.sessionId);
  }

  private sendToAttached(entry: ShellEntry, event: ShellEvent): void {
    if (entry.attachedTo === undefined) return;
    this.windows.get(entry.attachedTo)?.(event);
  }
}
