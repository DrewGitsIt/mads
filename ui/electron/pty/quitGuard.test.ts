// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { describeBusyShells, QuitGuard, type QuitGuardDeps } from "./quitGuard";
import type { ShellActivity } from "./shellHostCore";

const shell = (sessionId: string, commands: string[]): ShellActivity => ({
  sessionId,
  shellId: `shell-${sessionId}`,
  busy: commands.length > 0,
  foreground: undefined,
  processes: commands.map((command, i) => ({ pid: 10 + i, command })),
  count: commands.length,
  rssKb: 0,
});

function makeGuard(over: Partial<QuitGuardDeps> = {}) {
  const deps = {
    hostRunning: vi.fn(() => true),
    inspect: vi.fn(async () => [shell("ses_a", [])]),
    confirm: vi.fn(async () => true),
    killHost: vi.fn(),
    quit: vi.fn(),
    ...over,
  };
  const guard = new QuitGuard(deps);
  const event = () => ({ preventDefault: vi.fn() });
  const settle = () => new Promise((r) => setTimeout(r, 0));
  return { guard, deps, event, settle };
}

describe("QuitGuard", () => {
  it("prompts once when a shell is busy, and quits after 'Quit and end them'", async () => {
    const busy = shell("ses_a", ["sleep 30"]);
    const { guard, deps, event, settle } = makeGuard({
      inspect: vi.fn(async () => [busy, shell("ses_b", [])]),
    });
    const first = event();
    guard.handleBeforeQuit(first);
    expect(first.preventDefault).toHaveBeenCalled();
    await settle();
    expect(deps.confirm).toHaveBeenCalledTimes(1);
    expect(deps.confirm).toHaveBeenCalledWith([busy]);
    expect(deps.killHost).toHaveBeenCalledTimes(1);
    expect(deps.quit).toHaveBeenCalledTimes(1);
    // app.quit() fires before-quit again: it passes through untouched.
    const second = event();
    guard.handleBeforeQuit(second);
    expect(second.preventDefault).not.toHaveBeenCalled();
    expect(deps.confirm).toHaveBeenCalledTimes(1);
  });

  it("does nothing on Cancel, and asks again on the next quit", async () => {
    const { guard, deps, event, settle } = makeGuard({
      inspect: vi.fn(async () => [shell("ses_a", ["tail -f x"])]),
      confirm: vi.fn(async () => false),
    });
    guard.handleBeforeQuit(event());
    await settle();
    expect(deps.killHost).not.toHaveBeenCalled();
    expect(deps.quit).not.toHaveBeenCalled();
    const again = event();
    guard.handleBeforeQuit(again);
    expect(again.preventDefault).toHaveBeenCalled();
    await settle();
    expect(deps.confirm).toHaveBeenCalledTimes(2);
  });

  it("ignores a second quit request while the first is still being decided", async () => {
    let answer!: (ok: boolean) => void;
    const { guard, deps, event, settle } = makeGuard({
      inspect: vi.fn(async () => [shell("ses_a", ["x"])]),
      confirm: vi.fn(() => new Promise<boolean>((r) => (answer = r))),
    });
    guard.handleBeforeQuit(event());
    await settle();
    const second = event();
    guard.handleBeforeQuit(second);
    expect(second.preventDefault).toHaveBeenCalled();
    expect(deps.confirm).toHaveBeenCalledTimes(1);
    answer(true);
    await settle();
    expect(deps.quit).toHaveBeenCalledTimes(1);
  });

  it("quits without asking when every shell is idle", async () => {
    const { guard, deps, event, settle } = makeGuard();
    guard.handleBeforeQuit(event());
    await settle();
    expect(deps.confirm).not.toHaveBeenCalled();
    expect(deps.killHost).toHaveBeenCalled();
    expect(deps.quit).toHaveBeenCalled();
  });

  it("waits for the host shutdown before quitting", async () => {
    let done!: () => void;
    const { guard, deps, event, settle } = makeGuard({
      killHost: vi.fn(() => new Promise<void>((r) => (done = r))),
    });
    guard.handleBeforeQuit(event());
    await settle();
    expect(deps.killHost).toHaveBeenCalled();
    expect(deps.quit).not.toHaveBeenCalled();
    done();
    await settle();
    expect(deps.quit).toHaveBeenCalledTimes(1);
  });

  it("lets the quit through at once when there is no shell host", () => {
    const { guard, deps, event } = makeGuard({ hostRunning: vi.fn(() => false) });
    const e = event();
    guard.handleBeforeQuit(e);
    expect(e.preventDefault).not.toHaveBeenCalled();
    expect(deps.inspect).not.toHaveBeenCalled();
  });

  it("treats a host that never answers as idle after the timeout", async () => {
    let fire!: () => void;
    const cancel = vi.fn();
    const { guard, deps, event, settle } = makeGuard({
      inspect: vi.fn(() => new Promise<ShellActivity[]>(() => {})),
      schedule: (fn, ms) => {
        expect(ms).toBe(1500);
        fire = fn;
        return cancel;
      },
    });
    guard.handleBeforeQuit(event());
    await settle();
    expect(deps.quit).not.toHaveBeenCalled();
    fire();
    await settle();
    expect(deps.confirm).not.toHaveBeenCalled();
    expect(deps.killHost).toHaveBeenCalled();
    expect(deps.quit).toHaveBeenCalled();
  });

  it("treats a failed inspect as idle", async () => {
    const { guard, deps, event, settle } = makeGuard({
      inspect: vi.fn(async () => {
        throw new Error("host gone");
      }),
    });
    guard.handleBeforeQuit(event());
    await settle();
    expect(deps.confirm).not.toHaveBeenCalled();
    expect(deps.quit).toHaveBeenCalled();
  });
});

describe("describeBusyShells", () => {
  it("lists each shell by session with its first command and the rest as a count", () => {
    const fg: ShellActivity = { ...shell("ses_c", []), busy: true, foreground: "vim" };
    expect(
      describeBusyShells([shell("ses_a", ["sleep 30", "tail -f x", "top"]), shell("ses_b", ["make"]), fg]),
    ).toBe("ses_a: sleep 30 (+2 more)\nses_b: make\nses_c: vim");
  });
});
