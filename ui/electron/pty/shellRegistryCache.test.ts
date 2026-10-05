import { describe, expect, it, vi } from "vitest";
import { ShellRegistryCache } from "./shellRegistryCache";
import type { ShellInfo } from "./shellHostCore";

const info = (sessionId: string): ShellInfo => ({
  sessionId,
  shellId: `shell-${sessionId}`,
  cwd: "/",
  pid: 1,
  startedAt: 0,
  attached: false,
});

describe("ShellRegistryCache", () => {
  it("is empty until the host reports", () => {
    expect(new ShellRegistryCache(vi.fn()).get()).toEqual([]);
  });

  it("caches the host's list and announces each report", () => {
    const onChange = vi.fn();
    const cache = new ShellRegistryCache(onChange);

    cache.set([info("a")]);
    expect(cache.get()).toEqual([info("a")]);
    cache.set([info("a"), info("b")]);

    expect(onChange.mock.calls).toEqual([[[info("a")]], [[info("a"), info("b")]]]);
  });

  it("clearing on host exit empties the cache and announces it once", () => {
    const onChange = vi.fn();
    const cache = new ShellRegistryCache(onChange);
    cache.set([info("a")]);
    onChange.mockClear();

    cache.clear();
    cache.clear(); // nothing left to announce

    expect(cache.get()).toEqual([]);
    expect(onChange.mock.calls).toEqual([[[]]]);
  });
});
