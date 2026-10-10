import { randomUUID } from "node:crypto";
import { statSync } from "node:fs";
import { basename } from "node:path";
import { defaultShell, resolveCwd, type NodePtyModule, type PtyProcess } from "./ptyHost";
import { shellEnv } from "./shellEnv";
import { describeTree, type ProcessSource } from "./processTable";
import { createHeadlessMirror, type CreateMirror, type ShellMirror } from "./shellMirror";

export { SHELL_SCROLLBACK } from "./shellMirror";

/** How long attached output is batched before it is sent to the window. */
export const DATA_BATCH_MS = 16;

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
 *
 * Every shell also feeds a screen mirror (shellMirror.ts, D3) whether or not a
 * window is attached. Re-opening a live shell returns the mirror's snapshot in
 * the open result (see openExisting for why it can't be an event).
 */

export type ShellRequest =
  | { type: "open"; sessionId: string; cwd?: string; cols?: number; rows?: number }
  | { type: "detach"; shellId: string }
  | { type: "end"; shellId: string }
  | { type: "end-session"; sessionId: string }
  | { type: "inspect"; shellId?: string }
  | { type: "write"; shellId: string; data: string }
  | { type: "resize"; shellId: string; cols: number; rows: number }
  | { type: "list" };

/** Pushed to a window's port; only ever reaches the attached window. */
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
  /**
   * Only when created is false: the shell's screen, scrollback and modes as
   * a string to write into a fresh terminal of the open's size. It is in the
   * reply, not an event, because the window only learns the shellId from the
   * reply. Live `data` then continues exactly where the snapshot ends.
   */
  snapshot?: string;
}

/** What a request resolved or rejected with, as the glue posts it back. */
export type ShellOutcome = { ok: true; result: unknown } | { ok: false; error: string };

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

/**
 * What is running in one shell (D5), asked for on demand. `processes` are the
 * shell's descendants (the shell itself excluded); `busy` is also true when
 * the pty's foreground process is not the shell, even if no descendant could
 * be read.
 */
export interface ShellActivity {
  sessionId: string;
  shellId: string;
  busy: boolean;
  /** Something other than the shell owns the terminal's foreground. */
  foreground: string | undefined;
  processes: Array<{ pid: number; command: string }>;
  count: number;
  /** Resident memory of the shell and everything under it, in KiB. */
  rssKb: number;
}

export type SendToWindow = (event: ShellEvent) => void;

export interface ShellHostDeps {
  /** Resolves undefined when node-pty is unavailable (see loadNodePty). */
  loadPty: () => Promise<NodePtyModule | undefined>;
  env?: () => NodeJS.ProcessEnv;
  shell?: () => string;
  now?: () => number;
  newId?: () => string;
  /** The screen mirror factory; defaults to a real @xterm/headless one. */
  createMirror?: CreateMirror;
  /**
   * Called with the current list whenever a shell is created, ended or exits.
   * The glue relays it to main, which caches it for every window — windows
   * never hear it over their ports, so asking "which shells exist" doesn't
   * require connecting to (and forking) this host.
   */
  onShellsChanged?: (shells: ShellInfo[]) => void;
  /**
   * Sends a signal to a pid (process.kill in the real host). May throw (ESRCH,
   * EPERM). Deliberately has no default, like processSource: "end" hangs up a
   * shell's descendants, and a test that left the real process table and the
   * real process.kill in place behind a fake pty once SIGHUPed every process
   * the user owned (2026-10-06). Only shellHost.ts and the realtests pass the
   * real ones; everything else must pass fakes, and the compiler says so.
   */
  signal: (pid: number, signal: NodeJS.Signals) => void;
  /** A process source for one request (createProcessSource in the real host). No default; see `signal`. */
  processSource: () => ProcessSource;
  /** Whether a path is a directory on this machine; defaults to a stat. */
  isDirectory?: (path: string) => boolean;
  /** Runs `fn` after `ms` and returns a canceller; defaults to setTimeout. */
  schedule?: (fn: () => void, ms: number) => () => void;
}

interface ShellEntry {
  sessionId: string;
  shellId: string;
  proc: PtyProcess;
  /** The shell's own program name, to tell it apart from a foreground job. */
  shellName: string;
  cwd: string | undefined;
  startedAt: number;
  attachedTo: number | undefined;
  mirror: ShellMirror;
  /** Output waiting for the attached window's next batch flush. */
  pending: string[];
  cancelFlush: (() => void) | undefined;
  /**
   * Set from the cut of a re-open until its reply is posted: output for the
   * new window collects in `pending` and an exit waits in `heldExit`, so
   * nothing reaches the window before it knows the shellId.
   */
  holdToken: object | undefined;
  heldExit: number | undefined;
  /** Re-opens whose snapshot cut is still waiting on the mirror. */
  cuts: number;
  /** The shell ended while a cut was pending; the mirror dies when it finishes. */
  disposeAfterCuts: boolean;
}

/** An open's result plus the step that lets held output through, after the reply is posted. */
interface Dispatched {
  result: unknown;
  release?: () => void;
}

/** "-bash", "/bin/bash --login" and "bash" are all the program "bash". */
function programName(command: string): string {
  return basename(command.trim().split(/\s+/)[0] ?? "").replace(/^-/, "");
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
      if (entry.attachedTo === windowId) this.unhook(entry);
    }
  }

  /**
   * Answers a request. For a re-open this releases the output held behind the
   * snapshot as soon as the promise resolves, which is *before* the caller
   * can post the reply — use handleAndReply where reply order matters.
   */
  async handle(windowId: number, request: ShellRequest): Promise<unknown> {
    const { result, release } = await this.dispatch(windowId, request);
    release?.();
    return result;
  }

  /**
   * Like handle, but posts the answer itself through `reply`, and only then
   * lets output that arrived during a re-open's snapshot cut flow to the
   * window. The glue (shellHost.ts) uses this so the renderer sees the open
   * reply, and with it the shellId, before any `data` or `exit` that follows
   * the snapshot. `reply` must post synchronously.
   */
  async handleAndReply(
    windowId: number,
    request: ShellRequest,
    reply: (outcome: ShellOutcome) => void,
  ): Promise<void> {
    let release: (() => void) | undefined;
    try {
      const dispatched = await this.dispatch(windowId, request);
      release = dispatched.release;
      reply({ ok: true, result: dispatched.result });
    } catch (error) {
      reply({ ok: false, error: error instanceof Error ? error.message : String(error) });
    } finally {
      release?.();
    }
  }

  private async dispatch(windowId: number, request: ShellRequest): Promise<Dispatched> {
    if (!this.windows.has(windowId)) {
      throw new Error(`window ${windowId} is not connected to the shell host`);
    }
    switch (request.type) {
      case "open":
        return this.open(windowId, request);
      case "detach":
        // Only the attached window can detach; a stale view must not unhook a newer one.
        if (this.owned(windowId, request.shellId)) this.detach(request.shellId);
        return { result: undefined };
      case "end":
        await this.end(request.shellId);
        return { result: undefined };
      case "end-session": {
        const entry = this.bySession.get(request.sessionId);
        if (entry) await this.end(entry.shellId);
        return { result: undefined };
      }
      case "inspect":
        return { result: await this.inspect(request.shellId) };
      case "write":
        this.owned(windowId, request.shellId)?.proc.write(request.data);
        return { result: undefined };
      case "resize": {
        const entry = this.owned(windowId, request.shellId);
        if (entry) this.resize(entry, request.cols, request.rows);
        return { result: undefined };
      }
      case "list":
        return { result: this.list() };
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

  /**
   * D5: what is running in one shell, or in all of them. Reads the process
   * table now; nothing is cached or polled.
   */
  async inspect(shellId?: string): Promise<ShellActivity[]> {
    const entries = shellId ? [this.byId.get(shellId)] : [...this.byId.values()];
    const source = this.deps.processSource();
    const out: ShellActivity[] = [];
    for (const entry of entries) {
      if (!entry) continue;
      const pid = entry.proc.pid;
      const tree = pid ? await describeTree(source, pid) : { root: undefined, descendants: [] };
      let foreground: string | undefined;
      try {
        foreground = entry.proc.process;
      } catch {
        // node-pty throws once the pty is gone; then nothing is in the foreground.
      }
      // Whether it is the shell's own name: node-pty reports a path or argv0
      // ("/bin/sh", "-bash" for a login shell) as readily as a bare name.
      const names = new Set([entry.shellName, programName(tree.root?.command ?? "")]);
      const away = !!foreground && !names.has(programName(foreground));
      const processes = tree.descendants.map(({ pid: p, command }) => ({ pid: p, command }));
      // Foreground differs but nothing was readable (ps failed, or it exited
      // mid-walk): still say something is running, by the name node-pty gave.
      if (away && processes.length === 0) processes.push({ pid: 0, command: foreground as string });
      out.push({
        sessionId: entry.sessionId,
        shellId: entry.shellId,
        busy: away || processes.length > 0,
        foreground: away ? foreground : undefined,
        processes,
        count: processes.length,
        rssKb: [tree.root, ...tree.descendants].reduce((sum, p) => sum + (p?.rssKb ?? 0), 0),
      });
    }
    return out;
  }

  /** The shell, but only if this window is the one attached — others can't drive it. */
  private owned(windowId: number, shellId: string): ShellEntry | undefined {
    const entry = this.byId.get(shellId);
    return entry && entry.attachedTo === windowId ? entry : undefined;
  }

  private async open(
    windowId: number,
    request: Extract<ShellRequest, { type: "open" }>,
  ): Promise<Dispatched> {
    const nodePty = await this.deps.loadPty();
    if (!nodePty) {
      throw new Error("node-pty is unavailable on this machine");
    }
    // Everything up to the cut below is synchronous, so two concurrent opens
    // for one session can't both see "no shell" and spawn twice.
    const sink = this.windows.get(windowId);
    if (!sink) throw new Error(`window ${windowId} is not connected to the shell host`);

    const existing = this.bySession.get(request.sessionId);
    if (existing) return this.openExisting(windowId, existing, request);

    // A session's folder isn't always here: a landed worktree is removed
    // until the agent's next turn re-creates it, and a window on another
    // machine than the Router gets the Router's paths. node-pty can't start
    // in a missing folder (Linux: "chdir(2) failed", macOS: "posix_spawnp
    // failed"), so start in HOME and say why.
    const wanted = resolveCwd(request.cwd);
    const isDirectory = this.deps.isDirectory ?? isDirectoryOnDisk;
    const home = resolveCwd(undefined);
    const cwd = wanted === undefined || isDirectory(wanted) ? wanted : home;
    const cols = request.cols ?? 80;
    const rows = request.rows ?? 24;
    const shellFile = (this.deps.shell ?? defaultShell)();
    const proc = nodePty.spawn(shellFile, [], {
      name: "xterm-256color",
      cols,
      rows,
      cwd,
      env: shellEnv((this.deps.env ?? (() => process.env))()),
    });
    const entry: ShellEntry = {
      sessionId: request.sessionId,
      shellId: (this.deps.newId ?? randomUUID)(),
      proc,
      shellName: basename(shellFile),
      cwd,
      startedAt: (this.deps.now ?? Date.now)(),
      attachedTo: windowId,
      mirror: (this.deps.createMirror ?? createHeadlessMirror)(cols, rows),
      pending: [],
      cancelFlush: undefined,
      holdToken: undefined,
      heldExit: undefined,
      cuts: 0,
      disposeAfterCuts: false,
    };
    this.bySession.set(entry.sessionId, entry);
    this.byId.set(entry.shellId, entry);
    this.broadcastShellsChanged();

    if (cwd !== wanted) {
      // Ahead of the shell's first prompt, in the mirror as well as the live stream.
      const note = `\x1b[2m[${wanted} doesn't exist here; started in ${cwd ?? "the default folder"}]\x1b[0m\r\n`;
      entry.mirror.write(note);
      entry.pending.push(note);
      this.scheduleFlush(entry);
    }

    proc.onData((data) => {
      // The mirror sees every byte; the window only what arrives while it is attached.
      entry.mirror.write(data);
      if (entry.attachedTo === undefined) return;
      entry.pending.push(data);
      if (entry.holdToken === undefined) this.scheduleFlush(entry);
    });
    proc.onExit(({ exitCode }) => {
      // An `end` already removed the entry and nobody is waiting on it.
      if (this.byId.get(entry.shellId) !== entry) return;
      if (entry.attachedTo === undefined) {
        // Nobody was watching; remember it so the next open can say so.
        this.tombstones.set(entry.sessionId, { exitCode, exitedAt: (this.deps.now ?? Date.now)() });
      } else if (entry.holdToken !== undefined) {
        // A re-open's reply isn't out yet: the exit goes after it, behind the held data.
        entry.heldExit = exitCode;
      } else {
        this.flush(entry);
        this.sendToAttached(entry, { event: "exit", shellId: entry.shellId, exitCode });
      }
      this.remove(entry);
      this.broadcastShellsChanged();
    });
    const previousExit = this.tombstones.get(request.sessionId);
    this.tombstones.delete(request.sessionId);
    return {
      result: previousExit
        ? { shellId: entry.shellId, created: true, previousExit }
        : { shellId: entry.shellId, created: true },
    };
  }

  /**
   * Re-open of a live shell: the window gets the mirror's screen in the open
   * result. The cut has to be exact, and xterm parses writes asynchronously,
   * so serializing right now could miss queued data. Instead, at time T the
   * pty is resized (the SIGWINCH makes full-screen programs repaint) and an
   * empty write is queued on the mirror; its callback runs after every byte
   * before T is parsed and before any byte after T, so the mirror is resized
   * and serialized there. Output after T collects in `pending` (held: the
   * window can't be sent anything before it learns the shellId from the
   * reply) and goes out when `release` is called after the reply is posted.
   * Bytes before T are in the snapshot only, bytes after only in the live
   * stream. An exit during the cut is held the same way.
   */
  private async openExisting(
    windowId: number,
    entry: ShellEntry,
    request: Extract<ShellRequest, { type: "open" }>,
  ): Promise<Dispatched> {
    // Latest open wins: the window that had it learns it lost it, after any
    // output it was already owed.
    const previous = entry.attachedTo;
    if (previous !== undefined && previous !== windowId && entry.holdToken === undefined) {
      this.flush(entry);
      this.windows.get(previous)?.({ event: "detached", shellId: entry.shellId });
    }
    // Whatever was batched for the old view is in the snapshot instead.
    this.clearPending(entry);
    entry.attachedTo = windowId;
    const token = {};
    entry.holdToken = token;
    entry.heldExit = undefined;

    const resizeTo = request.cols && request.rows ? { cols: request.cols, rows: request.rows } : undefined;
    // The new view may be a different size than the one that left.
    if (resizeTo) entry.proc.resize(resizeTo.cols, resizeTo.rows);
    entry.cuts += 1;
    const snapshot = await new Promise<string>((resolve) => {
      entry.mirror.write("", () => {
        if (resizeTo) entry.mirror.resize(resizeTo.cols, resizeTo.rows);
        resolve(entry.mirror.serialize());
      });
    });
    entry.cuts -= 1;
    if (entry.disposeAfterCuts && entry.cuts === 0) entry.mirror.dispose();

    return {
      result: { shellId: entry.shellId, created: false, snapshot },
      release: () => {
        if (entry.holdToken !== token) return; // a newer open took over the hold
        entry.holdToken = undefined;
        if (entry.attachedTo !== windowId) {
          this.clearPending(entry);
          entry.heldExit = undefined;
          return;
        }
        this.flush(entry);
        if (entry.heldExit !== undefined) {
          const exitCode = entry.heldExit;
          entry.heldExit = undefined;
          this.sendToAttached(entry, { event: "exit", shellId: entry.shellId, exitCode });
        }
      },
    };
  }

  private resize(entry: ShellEntry, cols: number, rows: number): void {
    entry.proc.resize(cols, rows);
    // Queued behind the bytes already written so they parse at the old size.
    entry.mirror.write("", () => entry.mirror.resize(cols, rows));
  }

  private detach(shellId: string): void {
    const entry = this.byId.get(shellId);
    if (entry) this.unhook(entry);
  }

  /** The view is gone: whatever was batched for it is dropped, the mirror has it. */
  private unhook(entry: ShellEntry): void {
    entry.attachedTo = undefined;
    entry.holdToken = undefined;
    entry.heldExit = undefined;
    this.clearPending(entry);
  }

  /**
   * Forgets the shell at once (so open/list see it gone), then hangs it up.
   * The kernel's hangup when the pty closes reaches only the foreground
   * process group, and a shell resends it to its background jobs only if it
   * chooses to (bash and zsh do, dash doesn't). So the descendants are
   * collected first — once the shell dies they are reparented and no longer
   * findable — and each gets SIGHUP after the shell is killed. A job that
   * ignores SIGHUP (nohup) survives, by design (D6); nothing escalates to
   * SIGKILL.
   */
  private end(shellId: string): Promise<void> {
    const entry = this.byId.get(shellId);
    if (!entry) return Promise.resolve();
    this.remove(entry);
    // An end owes the window nothing, not even output held behind a re-open.
    entry.holdToken = undefined;
    entry.heldExit = undefined;
    this.clearPending(entry);
    this.broadcastShellsChanged();
    return this.hangUp(entry);
  }

  private async hangUp(entry: ShellEntry): Promise<void> {
    let descendants: number[] = [];
    try {
      const pid = entry.proc.pid;
      const source = this.deps.processSource();
      // Only walk a tree this host provably owns: the shell must be our own
      // direct child (node-pty forks it from this process). Every process
      // descends from pid 1, so a wrong root — pid 1, a fake pty's made-up
      // pid that happens to be real, a recycled pid — would otherwise turn
      // "end this shell" into SIGHUP for an arbitrary slice of the machine.
      // That happened once (2026-10-06): a test's fake pty had pid 1. Checked
      // before the kill, while the shell is still ours to ask about.
      if (pid && pid > 1 && (await source.parent(pid)) === process.pid) {
        const tree = await describeTree(source, pid);
        descendants = tree.descendants.map((p) => p.pid);
      }
    } catch {
      // Unreadable table: the shell is still killed, just without the extra hangups.
    } finally {
      entry.proc.kill();
    }
    for (const pid of descendants) {
      // Belt and braces on top of the ownership check: never init, never this
      // host, never main (our parent).
      if (pid <= 1 || pid === process.pid || pid === process.ppid) continue;
      try {
        this.deps.signal(pid, "SIGHUP");
      } catch {
        // ESRCH: already gone; EPERM: not ours to signal. Neither matters.
      }
    }
  }

  /** Ends every shell, e.g. when the host is shutting down. */
  async endAll(): Promise<void> {
    await Promise.all([...this.byId.values()].map((entry) => this.end(entry.shellId)));
  }

  private remove(entry: ShellEntry): void {
    this.byId.delete(entry.shellId);
    if (this.bySession.get(entry.sessionId) === entry) this.bySession.delete(entry.sessionId);
    if (entry.cuts > 0) {
      // A snapshot is still waiting on the mirror; it disposes it when done.
      entry.disposeAfterCuts = true;
    } else {
      entry.mirror.dispose();
    }
    // A held batch/exit stays for the pending release; otherwise nothing is owed.
    if (entry.holdToken === undefined) this.clearPending(entry);
  }

  private scheduleFlush(entry: ShellEntry): void {
    if (entry.cancelFlush) return;
    const schedule =
      this.deps.schedule ??
      ((fn, ms) => {
        const timer = setTimeout(fn, ms);
        return () => clearTimeout(timer);
      });
    entry.cancelFlush = schedule(() => {
      entry.cancelFlush = undefined;
      this.flush(entry);
    }, DATA_BATCH_MS);
  }

  /** Sends the batch now (one `data` event) and cancels its timer. */
  private flush(entry: ShellEntry): void {
    entry.cancelFlush?.();
    entry.cancelFlush = undefined;
    if (entry.pending.length === 0) return;
    const data = entry.pending.join("");
    entry.pending = [];
    this.sendToAttached(entry, { event: "data", shellId: entry.shellId, data });
  }

  private clearPending(entry: ShellEntry): void {
    entry.cancelFlush?.();
    entry.cancelFlush = undefined;
    entry.pending = [];
  }

  /** A shell started, ended or exited (not attach/detach: those don't change what exists). */
  private broadcastShellsChanged(): void {
    this.deps.onShellsChanged?.(this.list());
  }

  private sendToAttached(entry: ShellEntry, event: ShellEvent): void {
    if (entry.attachedTo === undefined) return;
    this.windows.get(entry.attachedTo)?.(event);
  }
}

function isDirectoryOnDisk(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}
