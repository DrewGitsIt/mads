import { afterEach, describe, expect, it, vi } from "vitest";
import { MAX_LINK_SESSION_IDS, RouterHTTPError } from "./client";
import { fetchAllLinksFor, fetchLinksFor, resetBulkLinksSupport } from "./fetchLinks";
import type { Link } from "./types";

const link = (id: string, g: string, s: string) => ({ id, granting_session_id: g, subject_session_id: s }) as Link;
const ids = (n: number) => Array.from({ length: n }, (_, i) => `s${i}`);
const noPerSession = vi.fn((_: string) => Promise.reject(new Error("use listLinks")));

afterEach(() => {
  resetBulkLinksSupport();
  noPerSession.mockClear();
});

describe("fetchLinksFor", () => {
  it("dedupes ids and sends one request per MAX_LINK_SESSION_IDS chunk", async () => {
    const listLinks = vi.fn((_: string[]) => Promise.resolve([] as Link[]));
    const chunks = await fetchLinksFor({ listLinks, getSessionLinks: noPerSession }, [...ids(450), "s0", "s1"]);
    expect(listLinks.mock.calls.map(([c]) => c.length)).toEqual([MAX_LINK_SESSION_IDS, MAX_LINK_SESSION_IDS, 50]);
    expect(chunks.map((c) => c.ids.length)).toEqual([200, 200, 50]);
    expect(new Set(chunks.flatMap((c) => c.ids)).size).toBe(450);
  });

  it("sends nothing for no ids", async () => {
    const listLinks = vi.fn(() => Promise.resolve([] as Link[]));
    expect(await fetchLinksFor({ listLinks, getSessionLinks: noPerSession }, [])).toEqual([]);
    expect(listLinks).not.toHaveBeenCalled();
  });

  it("settles each chunk on its own", async () => {
    const listLinks = vi.fn((chunk: string[]) =>
      chunk.includes("s0") ? Promise.reject(new Error("boom")) : Promise.resolve([link("l", "s200", "s201")]),
    );
    const chunks = await fetchLinksFor({ listLinks, getSessionLinks: noPerSession }, ids(250));
    expect(chunks.map((c) => c.result.status)).toEqual(["rejected", "fulfilled"]);
  });
});

describe("fetchAllLinksFor", () => {
  it("merges chunks, keeping a link that spans two chunks once", async () => {
    const span = link("span", "s0", "s300");
    const listLinks = vi.fn(() => Promise.resolve([span]));
    expect(await fetchAllLinksFor({ listLinks, getSessionLinks: noPerSession }, ids(301))).toEqual([span]);
    expect(listLinks).toHaveBeenCalledTimes(2);
  });

  it("rejects when any chunk fails", async () => {
    const listLinks = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error("offline"));
    await expect(fetchAllLinksFor({ listLinks, getSessionLinks: noPerSession }, ids(201))).rejects.toThrow("offline");
  });
});

describe("fetchLinksFor against a server without the bulk route", () => {
  const server = [link("ab", "a", "b"), link("bc", "b", "c"), link("xy", "x", "y")];
  const getSessionLinks = vi.fn((id: string) =>
    Promise.resolve(server.filter((l) => l.granting_session_id === id || l.subject_session_id === id)),
  );
  afterEach(() => getSessionLinks.mockClear());

  it.each([404, 405])("reads per id after a %i, merged and deduped, and stays per id", async (status) => {
    const listLinks = vi.fn(() => Promise.reject(new RouterHTTPError(status, "404 page not found")));
    const [chunk] = await fetchLinksFor({ listLinks, getSessionLinks }, ["a", "b", "c"]);
    expect(chunk.result).toEqual({ status: "fulfilled", value: [server[0], server[1]] });
    expect(getSessionLinks).toHaveBeenCalledTimes(3);

    await fetchAllLinksFor({ listLinks, getSessionLinks }, ["x"]);
    expect(listLinks).toHaveBeenCalledTimes(1);
    expect(getSessionLinks).toHaveBeenCalledTimes(4);
  });

  it("fails the chunk when any per-id read fails", async () => {
    const listLinks = vi.fn(() => Promise.reject(new RouterHTTPError(404, "")));
    getSessionLinks.mockRejectedValueOnce(new Error("offline"));
    const [chunk] = await fetchLinksFor({ listLinks, getSessionLinks }, ["a", "b"]);
    expect(chunk.result.status).toBe("rejected");
  });

  it.each([
    ["a 500", new RouterHTTPError(500, "boom")],
    ["a 400", new RouterHTTPError(400, "bad")],
    ["a network error", new TypeError("Failed to fetch")],
  ])("does not fall back on %s", async (_label, err) => {
    const listLinks = vi.fn(() => Promise.reject(err));
    const [chunk] = await fetchLinksFor({ listLinks, getSessionLinks }, ["a"]);
    expect(chunk.result).toEqual({ status: "rejected", reason: err });
    await fetchLinksFor({ listLinks, getSessionLinks }, ["a"]);
    expect(listLinks).toHaveBeenCalledTimes(2);
    expect(getSessionLinks).not.toHaveBeenCalled();
  });
});
