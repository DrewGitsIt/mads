import { describe, expect, it, vi } from "vitest";
import { ShellBridge } from "./shellBridge";
import { makeFakeShellHost } from "./fakeShellHost";

describe("ShellBridge", () => {
  it("spawn opens the session's shell with cwd and size", async () => {
    const { host } = makeFakeShellHost();
    const bridge = new ShellBridge(host, "ses_1");

    const id = await bridge.spawn({ cwd: "/repo", cols: 100, rows: 30 });

    expect(id).toBe("shell-ses_1");
    expect(host.open).toHaveBeenCalledWith("ses_1", { cwd: "/repo", cols: 100, rows: 30 });
  });

  it("delivers the snapshot as the first onData chunk, exactly once", async () => {
    const { host, emitData } = makeFakeShellHost();
    host.open.mockResolvedValueOnce({ shellId: "s1", created: false, snapshot: "SCREEN" });
    const bridge = new ShellBridge(host, "ses_1");
    const id = await bridge.spawn();

    const first = vi.fn();
    bridge.onData(id, first);
    expect(first.mock.calls).toEqual([["SCREEN"]]);

    emitData("s1", "live");
    expect(first.mock.calls).toEqual([["SCREEN"], ["live"]]);

    const second = vi.fn();
    bridge.onData(id, second);
    expect(second).not.toHaveBeenCalled();
  });

  it("delivers a dim previous-exit notice before anything else", async () => {
    const { host } = makeFakeShellHost();
    host.open.mockResolvedValueOnce({
      shellId: "s1",
      created: true,
      previousExit: { exitCode: 137, exitedAt: 1 },
    });
    const bridge = new ShellBridge(host, "ses_1");
    const id = await bridge.spawn();

    const chunks: string[] = [];
    bridge.onData(id, (d) => chunks.push(d));
    expect(chunks).toHaveLength(1);
    expect(chunks[0]).toContain("[previous shell exited with code 137]");
    expect(chunks[0]).toMatch(/^\x1b\[2m/);
  });

  it("kill detaches and never ends", async () => {
    const { host } = makeFakeShellHost();
    const bridge = new ShellBridge(host, "ses_1");
    const id = await bridge.spawn();

    bridge.kill(id);

    expect(host.detach).toHaveBeenCalledWith("shell-ses_1");
    expect(host.end).not.toHaveBeenCalled();
  });

  it("an overlapping remount keeps the shell attached: only the last kill detaches", async () => {
    const { host } = makeFakeShellHost();
    const bridge = new ShellBridge(host, "ses_1");
    const first = bridge.spawn();
    const second = bridge.spawn(); // StrictMode: the next mount opens before the old one's kill runs
    const id = await first;
    await second;

    bridge.kill(id); // the disposed first mount
    expect(host.detach).not.toHaveBeenCalled();
    bridge.kill(id); // the second mount unmounting
    expect(host.detach).toHaveBeenCalledTimes(1);
  });

  it("end ends the current shell", async () => {
    const { host } = makeFakeShellHost();
    const bridge = new ShellBridge(host, "ses_1");
    await bridge.spawn();

    bridge.end();

    expect(host.end).toHaveBeenCalledWith("shell-ses_1");
  });

  it("end before the open resolves waits for it, then ends the shell it started", async () => {
    const { host } = makeFakeShellHost();
    let resolveOpen!: (r: { shellId: string; created: boolean }) => void;
    host.open.mockReturnValueOnce(new Promise((r) => (resolveOpen = r)));
    const bridge = new ShellBridge(host, "ses_1");
    const spawned = bridge.spawn();

    bridge.end(); // the dock's × while the open is still in flight
    expect(host.end).not.toHaveBeenCalled();

    resolveOpen({ shellId: "late", created: true });
    await spawned;
    await vi.waitFor(() => expect(host.end).toHaveBeenCalledWith("late"));
  });

  it("end waits out a failed open without ending anything", async () => {
    const { host } = makeFakeShellHost();
    host.open.mockRejectedValueOnce(new Error("no pty"));
    const bridge = new ShellBridge(host, "ses_1");
    const spawned = bridge.spawn();
    bridge.end();
    await expect(spawned).rejects.toThrow("no pty");
    await Promise.resolve();
    expect(host.end).not.toHaveBeenCalled();
  });

  it("does not detach a shell that exited, was taken, or whose host died", async () => {
    const f = makeFakeShellHost();
    const bridge = new ShellBridge(f.host, "ses_1");
    const id = await bridge.spawn();
    bridge.onExit(id, () => {});
    f.emitExit(id, 0);
    bridge.kill(id);
    expect(f.host.detach).not.toHaveBeenCalled();

    const id2 = await bridge.spawn();
    const detached = vi.fn();
    bridge.onDetached(detached);
    f.emitDetached(id2);
    expect(detached).toHaveBeenCalledTimes(1);
    bridge.kill(id2);
    expect(f.host.detach).not.toHaveBeenCalled();

    const id3 = await bridge.spawn();
    bridge.onHostExited(() => {});
    f.emitHostExited();
    bridge.kill(id3);
    expect(f.host.detach).not.toHaveBeenCalled();
  });

  it("passes write and resize through", async () => {
    const { host } = makeFakeShellHost();
    const bridge = new ShellBridge(host, "ses_1");
    const id = await bridge.spawn();
    bridge.write(id, "ls\r");
    bridge.resize(id, 90, 20);
    expect(host.write).toHaveBeenCalledWith("shell-ses_1", "ls\r");
    expect(host.resize).toHaveBeenCalledWith("shell-ses_1", 90, 20);
  });
});
