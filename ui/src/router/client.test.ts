import { afterEach, describe, expect, it, vi } from "vitest";
import { ROUTER_BASE, routerGet, routerPost } from "./client";

describe("routerGet", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches the given path against the Router base URL", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const result = await routerGet<{ ok: boolean }>("/sessions");

    expect(result).toEqual({ ok: true });
    const calledUrl = fetchMock.mock.calls[0][0] as URL;
    expect(calledUrl.toString()).toBe(`${ROUTER_BASE}/sessions`);
  });

  it("appends query params, skipping undefined values", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await routerGet("/tasks", { session_id: "s1", status: undefined });

    const calledUrl = fetchMock.mock.calls[0][0] as URL;
    expect(calledUrl.searchParams.get("session_id")).toBe("s1");
    expect(calledUrl.searchParams.has("status")).toBe(false);
  });

  it("throws on a non-ok response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("boom", { status: 500 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(routerGet("/sessions")).rejects.toThrow(/500/);
  });
});

describe("routerPost", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("POSTs a JSON body and decodes the JSON response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "s1" }), { status: 201 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const result = await routerPost<{ id: string }>("/sessions", { name: "s1" });

    expect(result).toEqual({ id: "s1" });
    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(url.toString()).toBe(`${ROUTER_BASE}/sessions`);
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify({ name: "s1" }));
  });

  it("returns undefined for a 204 response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await routerPost("/sessions/s1/terminate");

    expect(result).toBeUndefined();
  });
});
