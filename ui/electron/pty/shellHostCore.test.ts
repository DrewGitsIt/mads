import { beforeEach, describe, expect, it } from "vitest";
import type { NodePtyModule, PtyProcess } from "./ptyHost";
import { ShellHostCore, type ShellEvent } from "./shellHostCore";

/**
 * The shell host core against a fake pty module: no node-pty, no Electron.
 * (A real-pty round trip lives in shellHost.realtest.ts.)
 */

class FakePty implements PtyProcess {
  killed = false;
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

function connectWindow(windowId: number): ShellEvent[] {
  const events: ShellEvent[] = [];
  core.connect(windowId, (e) => events.push(e));
  return events;
}

beforeEach(() => {
  spawned = [];
  ids = 0;
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
  });
});

const open = (windowId: number, sessionId: string, extra: { cwd?: string; cols?: number; rows?: number } = {}) =>
  core.handle(windowId, { type: "open", sessionId, ...extra }) as Promise<{
    shellId: string;
    created: boolean;
    previousExit?: { exitCode: number; exitedAt: number };
  }>;

describe("ShellHostCore", () => {
  it("starts one shell per session and re-open attaches", async () => {
    connectWindow(1);
    const first = await open(1, "ses_a", { cwd: "/work", cols: 100, rows: 30 });
    const again = await open(1, "ses_a");
    expect(first).toEqual({ shellId: "shell-1", created: true });
    expect(again).toEqual({ shellId: "shell-1", created: false });
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

  it("rejects open when node-pty is unavailable", async () => {
    core = new ShellHostCore({ loadPty: async () => undefined });
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
    expect(again).toEqual({ shellId, created: false });
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
    expect(w1).toEqual([{ event: "data", shellId, data: "hello" }]);
    expect(w2).toEqual([]);

    await core.handle(1, { type: "detach", shellId });
    spawned[0].emitData("while hidden");
    expect(w1).toHaveLength(1);
    expect(w2).toEqual([]);
  });

  it("latest open wins and the previous window gets detached", async () => {
    const w1 = connectWindow(1);
    const w2 = connectWindow(2);
    const { shellId } = await open(1, "ses_a");
    const stolen = await open(2, "ses_a", { cols: 90, rows: 20 });
    expect(stolen).toEqual({ shellId, created: false });
    expect(w1).toEqual([{ event: "detached", shellId }]);
    expect(spawned[0].sizes).toEqual([[90, 20]]);

    spawned[0].emitData("x");
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
});
