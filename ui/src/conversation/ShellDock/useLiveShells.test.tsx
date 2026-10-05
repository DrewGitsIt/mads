import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useLiveShells } from "./useLiveShells";
import { makeFakeShellHost } from "./fakeShellHost";

const info = (sessionId: string) => ({
  sessionId,
  shellId: `shell-${sessionId}`,
  cwd: "/",
  pid: 1,
  startedAt: 0,
  attached: false,
});

describe("useLiveShells", () => {
  afterEach(() => {
    delete (window as { mads?: unknown }).mads;
  });

  it("is empty outside Electron", () => {
    const { result } = renderHook(() => useLiveShells());
    expect(result.current).toEqual([]);
  });

  it("lists on mount and follows each pushed list, including the empty one when the host dies", async () => {
    const fake = makeFakeShellHost();
    fake.setShells([info("a")]);
    window.mads = { shell: fake.host };

    const { result } = renderHook(() => useLiveShells());
    await waitFor(() => expect(result.current.map((s) => s.sessionId)).toEqual(["a"]));

    act(() => fake.setShells([info("a"), info("b")]));
    expect(result.current).toHaveLength(2);

    act(() => fake.setShells([]));
    expect(result.current).toEqual([]);
  });

  it("never connects to the host: no open, and no port-backed call but list", async () => {
    const fake = makeFakeShellHost();
    window.mads = { shell: fake.host };

    const { result } = renderHook(() => useLiveShells());
    await waitFor(() => expect(fake.host.list).toHaveBeenCalledTimes(1));

    expect(result.current).toEqual([]);
    expect(fake.host.open).not.toHaveBeenCalled();
    expect(fake.host.onHostExited).not.toHaveBeenCalled();
  });

  it("a push beats a slower list() reply", async () => {
    const fake = makeFakeShellHost();
    let resolveList!: (v: ReturnType<typeof info>[]) => void;
    fake.host.list.mockReturnValueOnce(new Promise((r) => (resolveList = r)));
    window.mads = { shell: fake.host };

    const { result } = renderHook(() => useLiveShells());
    act(() => fake.setShells([info("new")]));
    await act(async () => resolveList([info("stale")]));

    expect(result.current.map((s) => s.sessionId)).toEqual(["new"]);
  });
});
