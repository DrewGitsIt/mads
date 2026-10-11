import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useConfig } from "./useConfig";
import { fixtureConfig } from "../../../router/fixtures";

describe("useConfig", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches config on mount and seeds saved/draft", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixtureConfig), { status: 200 })),
    );

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.saved).toEqual(fixtureConfig);
    expect(result.current.draft).toEqual(fixtureConfig);
  });

  it("save() PUTs the draft and updates saved on success", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureConfig), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ...fixtureConfig, palette: "space" }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, palette: "space" });
    });

    let ok = false;
    await act(async () => {
      ok = await result.current.save();
    });
    expect(ok).toBe(true);
    expect(result.current.saved?.palette).toBe("space");
    expect(result.current.fieldErrors).toEqual({});
  });

  it("save() reports every field error at once and leaves the draft untouched", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(fixtureConfig), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            errors: [
              { field: "concurrency_limit", message: "must be at least 1" },
              { field: "log_retention_days", message: "must be at least 1" },
            ],
          }),
          { status: 400 },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, concurrency_limit: -1, log_retention_days: -1 });
    });

    let ok = true;
    await act(async () => {
      ok = await result.current.save();
    });
    expect(ok).toBe(false);
    expect(Object.keys(result.current.fieldErrors)).toEqual(
      expect.arrayContaining(["concurrency_limit", "log_retention_days"]),
    );
    expect(result.current.draft?.concurrency_limit).toBe(-1);
    expect(result.current.saved?.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
  });

  // A PUT /config echo: the body it was sent, as the Router would return it.
  function echoingFetch(fail = false) {
    return vi.fn().mockImplementation((_input: URL | string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        if (fail) return Promise.resolve(new Response(JSON.stringify({ error: "boom" }), { status: 500 }));
        return Promise.resolve(new Response(init.body as string, { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
    });
  }

  function putBodies(fetchMock: ReturnType<typeof vi.fn>) {
    return fetchMock.mock.calls
      .filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT")
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
  }

  it("saveFields() PUTs saved + the patch — never unrelated draft edits", async () => {
    const fetchMock = echoingFetch();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, concurrency_limit: 9 });
    });
    let ok = false;
    await act(async () => {
      ok = await result.current.saveFields({ theme: "light" });
    });

    expect(ok).toBe(true);
    const [body] = putBodies(fetchMock);
    expect(body.theme).toBe("light");
    expect(body.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
    expect(result.current.saved?.theme).toBe("light");
    expect(result.current.saved?.concurrency_limit).toBe(fixtureConfig.concurrency_limit);
  });

  it("saveFields() merges into a dirty draft, keeping the other edits unsaved", async () => {
    vi.stubGlobal("fetch", echoingFetch());
    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    act(() => {
      result.current.setDraft({ ...result.current.draft!, concurrency_limit: 9 });
    });
    await act(async () => {
      await result.current.saveFields({ theme: "light" });
    });

    expect(result.current.draft?.theme).toBe("light");
    expect(result.current.draft?.concurrency_limit).toBe(9);
    expect(result.current.dirtyFields).toEqual(["concurrency_limit"]);
  });

  it("saveFields() never reads as an unsaved edit, even mid-flight", async () => {
    let release!: (r: Response) => void;
    const fetchMock = vi.fn().mockImplementation((_input: URL | string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        const body = init.body as string;
        return new Promise<Response>((resolve) => {
          release = () => resolve(new Response(body, { status: 200 }));
        });
      }
      return Promise.resolve(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    let pending!: Promise<boolean>;
    act(() => {
      pending = result.current.saveFields({ palette: "space" });
    });
    expect(result.current.draft?.palette).toBe("space");
    expect(result.current.dirtyFields).toEqual([]);

    await act(async () => {
      await waitFor(() => expect(release).toBeTypeOf("function"));
      release(new Response());
      await pending;
    });
    expect(result.current.saved?.palette).toBe("space");
  });

  it("saveFields() runs one at a time, each building on the last", async () => {
    const fetchMock = echoingFetch();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await Promise.all([
        result.current.saveFields({ palette: "space" }),
        result.current.saveFields({ theme: "light" }),
      ]);
    });

    const bodies = putBodies(fetchMock);
    expect(bodies[1]).toMatchObject({ palette: "space", theme: "light" });
    expect(result.current.saved).toMatchObject({ palette: "space", theme: "light" });
  });

  it("saveFields() rolls the patched field back on failure", async () => {
    vi.stubGlobal("fetch", echoingFetch(true));
    const { result } = renderHook(() => useConfig());
    await waitFor(() => expect(result.current.loading).toBe(false));

    let ok = true;
    await act(async () => {
      ok = await result.current.saveFields({ theme: "light" });
    });

    expect(ok).toBe(false);
    expect(result.current.saved?.theme).toBe(fixtureConfig.theme);
    expect(result.current.draft?.theme).toBe(fixtureConfig.theme);
    expect(result.current.dirtyFields).toEqual([]);
  });
});
