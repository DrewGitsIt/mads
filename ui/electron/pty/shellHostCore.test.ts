// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import { Terminal } from "@xterm/headless";
import type { NodePtyModule, PtyProcess } from "./ptyHost";
import {
  DATA_BATCH_MS,
  SHELL_SCROLLBACK,
  ShellHostCore,
  type ShellActivity,
  type ShellEvent,
  type ShellInfo,
} from "./shellHostCore";
import type { ProcessInfo, ProcessSource } from "./processTable";
import { createHeadlessMirror, type ShellMirror } from "./shellMirror";

/**
 * The shell host core against a fake pty module: no node-pty, no Electron.
 * (A real-pty round trip lives in shellHost.realtest.ts.) The screen mirror is
 * the real @xterm/headless one; the 16 ms batch timer is manual (`tick`).
 */

class FakePty implements PtyProcess {
  killed = false;
  /** The foreground process name node-pty would report. */
  process: string | undefined = "fakesh";
  written: string[] = [];
  sizes: Array<[number, number]> = [];
  private dataListeners: Array<(d: string) => void> = [];
  private exitListeners: Array<(e: { exitCode: number }) => void> = [];
  constructor(
    readonly pid: number,
    readonly file: string,
    readonly options: { cwd?: string; cols?: number; rows?: number; env?: Record<string, string | undefined> },
  ) {}
  write(data: string) {
    this.written.push(data);
  }
  resize(cols: number, rows: number) {
    this.sizes.push([cols, rows]);
  }
  kill() {
    this.killed = true;
    this.emitExit(0);
  }
  onData(l: (d: string) => void) {
    this.dataListeners.push(l);
    return { dispose() {} };
  }
  onExit(l: (e: { exitCode: number }) => void) {
    this.exitListeners.push(l);
    return { dispose() {} };
  }
  emitData(d: string) {
    this.dataListeners.forEach((l) => l(d));
  }
  emitExit(exitCode: number) {
    this.exitListeners.forEach((l) => l({ exitCode }));
  }
}

let spawned: FakePty[];
let core: ShellHostCore;
let ids = 0;
let timers: Array<{ fn: () => void; ms: number; live: boolean }>;
// The process table the host reads for `inspect`: parent pid -> children.
let processTable: Map<number, ProcessInfo[]>;
const fakeSource: ProcessSource = {
  children: async (pid) => (processTable.get(pid) ?? []).map((p) => p.pid),
  info: async (pid) =>
    pid >= 1000 && pid < 1100
      ? { pid, command: "/bin/fakesh", rssKb: 100 }
      : [...processTable.values()].flat().find((p) => p.pid === pid),
  // The fake shells (pids 1000-1099) are this process's children, so the
  // ownership check in end() lets their trees be walked; everything else is
  // somebody else's. A test can override a shell's parent via `parents`.
  parent: async (pid) => (parents.has(pid) ? parents.get(pid) : pid >= 1000 && pid < 1100 ? process.pid : 1),
};
// Per-test parent overrides for the fake source.
let parents: Map<number, number | undefined>;
// Signals the host sent, and an errno to throw for given pids.
let signals: Array<[number, string]>;
let signalErrors: Map<number, string>;
let mirrorHook: ((data: string) => void) | undefined;
// Folders the fake disk lacks; every other path is a directory.
let missingDirs: Set<string>;

/** Fires every scheduled batch flush that wasn't cancelled. */
function tick(): void {
  const due = timers.filter((t) => t.live);
  timers = [];
  due.forEach((t) => {
    t.live = false;
    t.fn();
  });
}

/** Real headless mirror that tells the test when an empty (cut) write is queued. */
function hookedMirror(cols: number, rows: number): ShellMirror {
  const real = createHeadlessMirror(cols, rows);
  return {
    ...real,
    write: (data, callback) => {
      real.write(data, callback);
      if (data === "") mirrorHook?.(data);
    },
  };
}

/** What a fresh terminal shows after being written `data`, as text. */
async function screenText(data: string, cols = 80, rows = 24): Promise<string> {
  const term = new Terminal({ cols, rows, scrollback: SHELL_SCROLLBACK, allowProposedApi: true });
  await new Promise<void>((resolve) => term.write(data, resolve));
  const buffer = term.buffer.active;
  const lines: string[] = [];
  for (let i = 0; i < buffer.length; i++) lines.push(buffer.getLine(i)?.translateToString(true) ?? "");
  term.dispose();
  return lines.join("\n");
}

// What the host reported to main via onShellsChanged, one entry per report.
let reported: string[][];

function connectWindow(windowId: number): ShellEvent[] {
  const events: ShellEvent[] = [];
  core.connect(windowId, (e) => events.push(e));
  return events;
}

beforeEach(() => {
  reported = [];
  spawned = [];
  ids = 0;
  timers = [];
  mirrorHook = undefined;
  missingDirs = new Set();
  processTable = new Map();
  parents = new Map();
  signals = [];
  signalErrors = new Map();
  const pty: NodePtyModule = {
    spawn: (file, _args, options) => {
      const proc = new FakePty(1000 + spawned.length, file, options);
      spawned.push(proc);
      return proc;
    },
  };
  core = new ShellHostCore({
    loadPty: async () => pty,
    env: () => ({ PATH: "/bin", npm_config_prefix: "x" }),
    shell: () => "/bin/fakesh",
    now: () => 4242,
    newId: () => `shell-${++ids}`,
    createMirror: hookedMirror,
    isDirectory: (path) => !missingDirs.has(path),
    processSource: () => fakeSource,
    signal: (pid, sig) => {
      const code = signalErrors.get(pid);
      if (code) throw Object.assign(new Error(code), { code });
      signals.push([pid, sig]);
    },
    onShellsChanged: (shells) => reported.push(shells.map((x) => x.sessionId)),
    schedule: (fn, ms) => {
      const timer = { fn, ms, live: true };
      timers.push(timer);
      return () => {
        timer.live = false;
      };
    },
  });
});

const open = (windowId: number, sessionId: string, extra: { cwd?: string; cols?: number; rows?: number } = {}) =>
  core.handle(windowId, { type: "open", sessionId, ...extra }) as Promise<{
    shellId: string;
    created: boolean;
    previousExit?: { exitCode: number; exitedAt: number };
    snapshot?: string;
  }>;

describe("ShellHostCore", () => {
  it("starts one shell per session and re-open attaches", async () => {
    connectWindow(1);
    const first = await open(1, "ses_a", { cwd: "/work", cols: 100, rows: 30 });
    const again = await open(1, "ses_a");
    expect(first).toEqual({ shellId: "shell-1", created: true });
    expect(again).toMatchObject({ shellId: "shell-1", created: false });
    expect(spawned).toHaveLength(1);
    expect(spawned[0].file).toBe("/bin/fakesh");
    expect(spawned[0].options).toMatchObject({ cwd: "/work", cols: 100, rows: 30 });
    // shellEnv() scrubbing applied
    expect(spawned[0].options.env).toEqual({ PATH: "/bin", TERM_PROGRAM: "mads" });

    const other = await open(1, "ses_b");
    expect(other.created).toBe(true);
    expect(spawned).toHaveLength(2);
  });

  it("starts only one shell for concurrent opens of a session", async () => {
    connectWindow(1);
    const [a, b] = await Promise.all([open(1, "ses_a"), open(1, "ses_a")]);
    expect([a.created, b.created].sort()).toEqual([false, true]);
    expect(spawned).toHaveLength(1);
  });

  it("falls back to an absolute cwd for a relative one", async () => {
    connectWindow(1);
    await open(1, "ses_a", { cwd: "." });
    expect(spawned[0].options.cwd).toBe(process.env.HOME);
  });

  it("starts in HOME, and says so, when the cwd doesn't exist here", async () => {
    missingDirs.add("/gone/worktree");
    const events = connectWindow(1);
    const { shellId } = await open(1, "ses_a", { cwd: "/gone/worktree" });
    expect(spawned[0].options.cwd).toBe(process.env.HOME);
    spawned[0].emitData("$ ");
    tick();
    const shown = events.map((e) => (e.event === "data" ? e.data : "")).join("");
    expect(shown).toMatch(/^\x1b\[2m\[\/gone\/worktree doesn't exist here; started in .*\]\x1b\[0m\r\n\$ $/);
    expect(events.every((e) => e.event === "data" && e.shellId === shellId)).toBe(true);
    expect((await core.handle(1, { type: "list" })) as ShellInfo[]).toMatchObject([{ cwd: process.env.HOME }]);
  });

  it("keeps the missing-folder note in the snapshot a reattach gets", async () => {
    missingDirs.add("/gone/worktree");
    connectWindow(1);
    const { shellId } = await open(1, "ses_a", { cwd: "/gone/worktree", cols: 80, rows: 10 });
    await core.handle(1, { type: "detach", shellId });
    const again = await open(1, "ses_a", { cols: 80, rows: 10 });
    expect(await screenText(again.snapshot ?? "", 80, 10)).toContain("/gone/worktree doesn't exist here");
  });

  it("uses an existing cwd as given, with no note", async () => {
    const events = connectWindow(1);
    await open(1, "ses_a", { cwd: "/work" });
    tick();
    expect(spawned[0].options.cwd).toBe("/work");
    expect(events).toEqual([]);
  });

  it("rejects open when node-pty is unavailable", async () => {
    core = new ShellHostCore({
      loadPty: async () => undefined,
      processSource: () => fakeSource,
      signal: (pid, sig) => signals.push([pid, sig]),
    });
    connectWindow(1);
    await expect(open(1, "ses_a")).rejects.toThrow(/node-pty is unavailable/);
  });

  it("rejects requests from a window that never connected", async () => {
    await expect(open(9, "ses_a")).rejects.toThrow(/not connected/);
  });

  it("detach keeps the process alive and the shell listed", async () => {
    connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    expect(spawned[0].killed).toBe(false);
    expect(core.list()).toMatchObject([{ shellId, attached: false }]);
    const again = await open(1, "ses_a");
    expect(again).toMatchObject({ shellId, created: false });
    expect(spawned).toHaveLength(1);
  });

  it("end kills the shell and removes it, so the next open starts fresh", async () => {
    const events = connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "end", shellId });
    expect(spawned[0].killed).toBe(true);
    expect(core.list()).toEqual([]);
    // the kill's own exit is not reported as an unexpected exit
    expect(events.filter((e) => e.event === "exit")).toEqual([]);
    expect((await open(1, "ses_a")).created).toBe(true);
    expect(spawned).toHaveLength(2);
  });

  it("delivers data only to the attached window", async () => {
    const w1 = connectWindow(1);
    const w2 = connectWindow(2);
    const { shellId } = await open(1, "ses_a");
    spawned[0].emitData("hello");
    tick();
    expect(w1).toEqual([{ event: "data", shellId, data: "hello" }]);
    expect(w2).toEqual([]);

    await core.handle(1, { type: "detach", shellId });
    spawned[0].emitData("while hidden");
    tick();
    expect(w1).toHaveLength(1);
    expect(w2).toEqual([]);
  });

  it("latest open wins and the previous window gets detached", async () => {
    const w1 = connectWindow(1);
    const w2 = connectWindow(2);
    const { shellId } = await open(1, "ses_a");
    const stolen = await open(2, "ses_a", { cols: 90, rows: 20 });
    expect(stolen).toMatchObject({ shellId, created: false });
    expect(w1).toEqual([{ event: "detached", shellId }]);
    expect(spawned[0].sizes).toEqual([[90, 20]]);

    spawned[0].emitData("x");
    tick();
    expect(w1).toHaveLength(1);
    expect(w2).toEqual([{ event: "data", shellId, data: "x" }]);

    // the old window can no longer drive or detach it
    await core.handle(1, { type: "write", shellId, data: "nope" });
    await core.handle(1, { type: "detach", shellId });
    expect(spawned[0].written).toEqual([]);
    expect(core.list()[0].attached).toBe(true);

    await core.handle(2, { type: "write", shellId, data: "yes" });
    await core.handle(2, { type: "resize", shellId, cols: 50, rows: 10 });
    expect(spawned[0].written).toEqual(["yes"]);
    expect(spawned[0].sizes.at(-1)).toEqual([50, 10]);
  });

  it("re-open from the same window sends no detached event", async () => {
    const w1 = connectWindow(1);
    await open(1, "ses_a");
    await open(1, "ses_a");
    expect(w1).toEqual([]);
  });

  it("shell exit notifies the attached window and removes the shell", async () => {
    const events = connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    spawned[0].emitExit(7);
    expect(events).toEqual([{ event: "exit", shellId, exitCode: 7 }]);
    expect(core.list()).toEqual([]);
    expect((await open(1, "ses_a")).created).toBe(true);
  });

  it("reports the current list to main when a shell is created, ended or exits", async () => {
    connectWindow(1);

    const { shellId } = await open(1, "ses_a");
    expect(reported).toEqual([["ses_a"]]);

    // Re-open and detach change no shell's existence.
    await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    expect(reported).toHaveLength(1);

    await open(1, "ses_b");
    expect(reported.at(-1)).toEqual(["ses_a", "ses_b"]);

    await core.handle(1, { type: "end", shellId });
    expect(reported.at(-1)).toEqual(["ses_b"]);

    spawned[1].emitExit(0);
    expect(reported.at(-1)).toEqual([]);
    expect(reported).toHaveLength(4);
  });

  it("an exit that nobody was watching is reported too", async () => {
    connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    spawned[0].emitExit(0);
    expect(reported.at(-1)).toEqual([]);
  });

  it("sends nothing over window ports for a change in the shell set", async () => {
    const other = connectWindow(2);
    connectWindow(1);
    await open(1, "ses_a");
    expect(other).toEqual([]);
  });

  it("exit of a detached shell removes it silently", async () => {
    const events = connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    spawned[0].emitExit(0);
    expect(events).toEqual([]);
    expect(core.list()).toEqual([]);
  });

  it("reports a detached exit once, on the next open", async () => {
    connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    spawned[0].emitExit(3);
    expect(core.list()).toEqual([]);

    const next = await open(1, "ses_a");
    expect(next.created).toBe(true);
    expect(next.previousExit).toEqual({ exitCode: 3, exitedAt: 4242 });
    expect(spawned).toHaveLength(2);

    // consumed: the replacement dying later, attached, leaves nothing behind
    spawned[1].emitExit(0);
    const third = await open(1, "ses_a");
    expect(third.created).toBe(true);
    expect(third.previousExit).toBeUndefined();
  });

  it("leaves no tombstone for an attached exit", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    spawned[0].emitExit(1);
    expect((await open(1, "ses_a")).previousExit).toBeUndefined();
  });

  it("leaves no tombstone for an end", async () => {
    connectWindow(1);
    const { shellId } = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId });
    await core.handle(1, { type: "end", shellId });
    expect((await open(1, "ses_a")).previousExit).toBeUndefined();
  });

  it("consumed tombstone is per session", async () => {
    connectWindow(1);
    const a = await open(1, "ses_a");
    await core.handle(1, { type: "detach", shellId: a.shellId });
    spawned[0].emitExit(2);
    expect((await open(1, "ses_b")).previousExit).toBeUndefined();
    expect((await open(1, "ses_a")).previousExit?.exitCode).toBe(2);
  });

  it("disconnect detaches a window's shells without killing them", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    core.disconnect(1);
    expect(spawned[0].killed).toBe(false);
    expect(core.list()[0].attached).toBe(false);
    // a reloaded window (same id) reconnects and re-attaches
    connectWindow(1);
    expect((await open(1, "ses_a")).created).toBe(false);
  });

  it("list reports session, shell, cwd, start time and attachment", async () => {
    connectWindow(1);
    const a = await open(1, "ses_a", { cwd: "/a" });
    const b = await open(1, "ses_b", { cwd: "/b" });
    await core.handle(1, { type: "detach", shellId: b.shellId });
    expect(await core.handle(1, { type: "list" })).toEqual([
      { sessionId: "ses_a", shellId: a.shellId, cwd: "/a", pid: 1000, startedAt: 4242, attached: true },
      { sessionId: "ses_b", shellId: b.shellId, cwd: "/b", pid: 1001, startedAt: 4242, attached: false },
    ]);
  });

  describe("mirror and snapshot", () => {
    it("restores what arrived while detached, exactly once, then streams live after the reply", async () => {
      const events = connectWindow(1);
      const { shellId } = await open(1, "ses_a", { cols: 40, rows: 10 });
      spawned[0].emitData("A-first\r\n");
      tick();
      await core.handle(1, { type: "detach", shellId });
      spawned[0].emitData("B-hidden\r\n");
      spawned[0].emitData("C-hidden\r\n");
      tick();
      events.length = 0;

      await core.handleAndReply(1, { type: "open", sessionId: "ses_a", cols: 40, rows: 10 }, (outcome) =>
        events.push({ event: "data", shellId: "reply", data: JSON.stringify(outcome) }),
      );
      // data arriving after the reply is delivered live, not in the snapshot
      spawned[0].emitData("D-live\r\n");
      tick();

      const reply = events[0];
      expect(reply).toMatchObject({ shellId: "reply" });
      const { result } = JSON.parse((reply as { data: string }).data) as {
        result: { shellId: string; created: boolean; snapshot: string };
      };
      expect(result).toMatchObject({ shellId, created: false });
      const text = await screenText(result.snapshot, 40, 10);
      for (const needle of ["A-first", "B-hidden", "C-hidden"]) {
        expect(text.split(needle)).toHaveLength(2);
      }
      expect(text).not.toContain("D-live");
      expect(events.slice(1)).toEqual([{ event: "data", shellId, data: "D-live\r\n" }]);
    });

    it("sends no snapshot for a created shell", async () => {
      connectWindow(1);
      expect((await open(1, "ses_a")).snapshot).toBeUndefined();
    });

    it("a chunk arriving during the cut shows up exactly once across snapshot and live", async () => {
      const events = connectWindow(1);
      const { shellId } = await open(1, "ses_a", { cols: 40, rows: 10 });
      spawned[0].emitData("before\r\n");
      await core.handle(1, { type: "detach", shellId });
      // The chunk lands after the cut's write is queued but before its callback.
      mirrorHook = () => {
        mirrorHook = undefined;
        spawned[0].emitData("during\r\n");
      };
      let snapshot = "";
      await core.handleAndReply(1, { type: "open", sessionId: "ses_a" }, (outcome) => {
        snapshot = (outcome as { ok: true; result: { snapshot: string } }).result.snapshot;
      });
      tick();
      const live = events.filter((e) => e.event === "data").map((e) => (e as { data: string }).data);

      const text = await screenText(snapshot, 40, 10);
      expect(text).toContain("before");
      expect(text).not.toContain("during");
      expect(live).toEqual(["during\r\n"]);
    });

    it("an exit during the cut is delivered after the reply, behind the held data", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "detach", shellId });
      mirrorHook = () => {
        mirrorHook = undefined;
        spawned[0].emitData("last words\r\n");
        spawned[0].emitExit(9);
      };
      const log: string[] = [];
      core.connect(1, (e) => log.push(e.event));
      await core.handleAndReply(1, { type: "open", sessionId: "ses_a" }, () => log.push("reply"));
      expect(log).toEqual(["reply", "data", "exit"]);
      expect(core.list()).toEqual([]);
    });

    it("re-open at a new size resizes the pty and the snapshot reflects it", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a", { cols: 80, rows: 24 });
      spawned[0].emitData("x".repeat(60) + "\r\n");
      await core.handle(1, { type: "detach", shellId });
      const again = await open(1, "ses_a", { cols: 30, rows: 12 });
      expect(spawned[0].sizes).toEqual([[30, 12]]);
      const term = new Terminal({ cols: 30, rows: 12, scrollback: SHELL_SCROLLBACK, allowProposedApi: true });
      await new Promise<void>((resolve) => term.write(again.snapshot ?? "", resolve));
      // 60 x's reflowed onto two 30-column rows
      expect(term.buffer.active.getLine(0)?.translateToString(true)).toBe("x".repeat(30));
      expect(term.buffer.active.getLine(1)?.translateToString(true)).toBe("x".repeat(30));
      term.dispose();
    });

    it("keeps the alternate screen across a re-open", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a", { cols: 20, rows: 5 });
      spawned[0].emitData("normal\r\n\x1b[?1049h\x1b[Halt-screen");
      await core.handle(1, { type: "detach", shellId });
      const again = await open(1, "ses_a", { cols: 20, rows: 5 });
      const term = new Terminal({ cols: 20, rows: 5, allowProposedApi: true });
      await new Promise<void>((resolve) => term.write(again.snapshot ?? "", resolve));
      expect(term.buffer.active.type).toBe("alternate");
      expect(term.buffer.active.getLine(0)?.translateToString(true)).toBe("alt-screen");
      term.dispose();
    });

    it("disposes the mirror when the shell ends or exits", async () => {
      let disposed = 0;
      // Any core built here must inject processSource and signal: the defaults
      // read the real /proc and call process.kill, and "end" hangs up the
      // shell's descendants. With pid 1 that once SIGHUPed every process the
      // user owned and killed their desktop session.
      core = new ShellHostCore({
        processSource: () => fakeSource,
        signal: (pid, sig) => signals.push([pid, sig]),
        loadPty: async () => ({
          spawn: (file, _a, options) => {
            const proc = new FakePty(1000 + spawned.length, file, options);
            spawned.push(proc);
            return proc;
          },
        }),
        createMirror: (c, r) => {
          const real = createHeadlessMirror(c, r);
          return { ...real, dispose: () => (disposed++, real.dispose()) };
        },
      });
      connectWindow(1);
      const a = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId: a.shellId });
      await open(1, "ses_b");
      spawned[1].emitExit(0);
      expect(disposed).toBe(2);
    });
  });

  describe("batching", () => {
    it("coalesces chunks within a frame into one data event on one timer", async () => {
      const events = connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      spawned[0].emitData("a");
      spawned[0].emitData("b");
      spawned[0].emitData("c");
      expect(events).toEqual([]);
      expect(timers.filter((t) => t.live)).toHaveLength(1);
      expect(timers[0].ms).toBe(DATA_BATCH_MS);
      tick();
      expect(events).toEqual([{ event: "data", shellId, data: "abc" }]);
      spawned[0].emitData("d");
      tick();
      expect(events).toHaveLength(2);
    });

    it("flushes pending data before exit", async () => {
      const events = connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      spawned[0].emitData("bye");
      spawned[0].emitExit(2);
      expect(events).toEqual([
        { event: "data", shellId, data: "bye" },
        { event: "exit", shellId, exitCode: 2 },
      ]);
      tick();
      expect(events).toHaveLength(2);
    });

    it("flushes pending data before detached when another window steals the shell", async () => {
      const w1 = connectWindow(1);
      connectWindow(2);
      const { shellId } = await open(1, "ses_a");
      spawned[0].emitData("owed");
      await open(2, "ses_a");
      expect(w1).toEqual([
        { event: "data", shellId, data: "owed" },
        { event: "detached", shellId },
      ]);
    });

    it("drops the pending batch when its window detaches", async () => {
      const events = connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      spawned[0].emitData("unseen");
      await core.handle(1, { type: "detach", shellId });
      tick();
      expect(events).toEqual([]);
    });
  });
});

describe("ShellHostCore inspect (D5)", () => {
  const inspect = (shellId?: string) =>
    core.handle(1, { type: "inspect", shellId }) as Promise<ShellActivity[]>;

  it("reports a shell at its prompt as idle", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    const [activity] = await inspect();
    expect(activity).toMatchObject({
      sessionId: "ses_a",
      shellId: "shell-1",
      busy: false,
      processes: [],
      count: 0,
      rssKb: 100,
    });
  });

  it("is busy when the foreground process is not the shell, and lists the descendants", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    spawned[0].process = "python";
    processTable.set(1000, [{ pid: 2001, command: "python -m http.server", rssKb: 5000 }]);
    const [activity] = await inspect("shell-1");
    expect(activity).toMatchObject({
      busy: true,
      foreground: "python",
      processes: [{ pid: 2001, command: "python -m http.server" }],
      count: 1,
      rssKb: 5100,
    });
  });

  it("is busy for a background job even while the shell itself is in the foreground", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    processTable.set(1000, [{ pid: 2001, command: "sleep 30", rssKb: 10 }]);
    const [activity] = await inspect();
    expect(activity).toMatchObject({ busy: true, foreground: undefined, count: 1 });
  });

  it("treats a login shell's leading dash as the shell itself", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    spawned[0].process = "-fakesh";
    expect((await inspect())[0].busy).toBe(false);
    spawned[0].process = "/bin/fakesh"; // or the path it was spawned by
    expect((await inspect())[0].busy).toBe(false);
  });

  it("still says busy by the foreground name when no descendant could be read", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    spawned[0].process = "vim";
    const [activity] = await inspect();
    expect(activity).toMatchObject({ busy: true, count: 1, processes: [{ pid: 0, command: "vim" }] });
  });

  it("inspects every shell by default, and an unknown shell id yields nothing", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    await open(1, "ses_b");
    expect((await inspect()).map((a) => a.sessionId)).toEqual(["ses_a", "ses_b"]);
    expect(await inspect("nope")).toEqual([]);
  });

  it("ends a session's shell by session id, and ignores a session with none", async () => {
    connectWindow(1);
    await open(1, "ses_a");
    await core.handle(1, { type: "end-session", sessionId: "ses_none" });
    expect(spawned[0].killed).toBe(false);
    await core.handle(1, { type: "end-session", sessionId: "ses_a" });
    expect(spawned[0].killed).toBe(true);
    expect(core.list()).toEqual([]);
  });

  describe("end hangs up the descendants", () => {
    beforeEach(() => {
      processTable.set(1000, [
        { pid: 2001, command: "sudo svc", rssKb: 1 },
        { pid: 2002, command: "sleep 30", rssKb: 1 },
      ]);
      processTable.set(2001, [{ pid: 2003, command: "svc", rssKb: 1 }]);
    });

    it("kills the shell, then SIGHUPs every descendant, nested ones included", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId });
      expect(spawned[0].killed).toBe(true);
      expect(signals).toEqual([
        [2001, "SIGHUP"],
        [2002, "SIGHUP"],
        [2003, "SIGHUP"],
      ]);
    });

    it("forgets the shell at once, before the walk finishes", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      const ending = core.handle(1, { type: "end", shellId });
      expect(core.list()).toEqual([]);
      await ending;
    });

    it("end-session and endAll do the same for every shell", async () => {
      connectWindow(1);
      await open(1, "ses_a");
      await open(1, "ses_b");
      await core.handle(1, { type: "end-session", sessionId: "ses_a" });
      expect(spawned[0].killed).toBe(true);
      expect(signals.map(([pid]) => pid)).toEqual([2001, 2002, 2003]);
      signals.length = 0;
      await core.endAll();
      expect(spawned[1].killed).toBe(true);
      expect(core.list()).toEqual([]);
    });

    it("ignores ESRCH and EPERM and keeps signalling the rest", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      signalErrors.set(2001, "ESRCH");
      signalErrors.set(2002, "EPERM");
      await core.handle(1, { type: "end", shellId });
      expect(signals).toEqual([[2003, "SIGHUP"]]);
    });

    it("still kills the shell when the process table can't be read", async () => {
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      processTable = undefined as unknown as typeof processTable; // the fake source throws
      await core.handle(1, { type: "end", shellId });
      expect(spawned[0].killed).toBe(true);
      expect(signals).toEqual([]);
    });

    it("never walks from pid 1, whose descendants are every process on the machine", async () => {
      processTable.set(1, [{ pid: 2278, command: "systemd --user", rssKb: 1 }]);
      // Even if the ownership check were fooled, pid 1 is never a root.
      parents.set(1, process.pid);
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      (spawned[0] as { pid: number }).pid = 1;
      await core.handle(1, { type: "end", shellId });
      expect(spawned[0].killed).toBe(true);
      expect(signals).toEqual([]);
    });

    it("signals nothing when the shell isn't this host's own child", async () => {
      // e.g. a recycled pid, or a fake pty whose made-up pid is somebody's real process.
      processTable.set(1000, [{ pid: 2001, command: "gnome-shell", rssKb: 1 }]);
      parents.set(1000, 4321);
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId });
      expect(spawned[0].killed).toBe(true);
      expect(signals).toEqual([]);
    });

    it("signals nothing when the shell's parent can't be read", async () => {
      processTable.set(1000, [{ pid: 2001, command: "sleep 30", rssKb: 1 }]);
      parents.set(1000, undefined);
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId });
      expect(signals).toEqual([]);
    });

    it("never hangs up main (the host's parent)", async () => {
      processTable.set(1000, [{ pid: process.ppid, command: "electron", rssKb: 1 }]);
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId });
      expect(signals).toEqual([]);
    });

    it("never hangs up the host itself", async () => {
      processTable.set(1000, [{ pid: process.pid, command: "node", rssKb: 1 }]);
      connectWindow(1);
      const { shellId } = await open(1, "ses_a");
      await core.handle(1, { type: "end", shellId });
      expect(signals).toEqual([]);
    });
  });
});
