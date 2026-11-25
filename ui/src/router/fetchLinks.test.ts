import { describe, expect, it, vi } from "vitest";
import { MAX_LINK_SESSION_IDS, RouterHTTPError } from "./client";
import { fetchAllLinksFor, fetchLinksFor } from "./fetchLinks";
import type { Link } from "./types";

const link = (id: string, g: string, s: string) => ({ id, granting_session_id: g, subject_session_id: s }) as Link;
const ids = (n: number) => Array.from({ length: n }, (_, i) => `s${i}`);

describe("fetchLinksFor", () => {
  it("dedupes ids and sends one request per MAX_LINK_SESSION_IDS chunk", async () => {
    const listLinks = vi.fn((_: string[]) => Promise.resolve([] as Link[]));
    const chunks = await fetchLinksFor({ listLinks }, [...ids(450), "s0", "s1"]);
    expect(listLinks.mock.calls.map(([c]) => c.length)).toEqual([MAX_LINK_SESSION_IDS, MAX_LINK_SESSION_IDS, 50]);
    expect(chunks.map((c) => c.ids.length)).toEqual([200, 200, 50]);
    expect(new Set(chunks.flatMap((c) => c.ids)).size).toBe(450);
  });

  it("sends nothing for no ids", async () => {
    const listLinks = vi.fn(() => Promise.resolve([] as Link[]));
    expect(await fetchLinksFor({ listLinks }, [])).toEqual([]);
    expect(listLinks).not.toHaveBeenCalled();
  });

  it("settles each chunk on its own", async () => {
    const listLinks = vi.fn((chunk: string[]) =>
      chunk.includes("s0") ? Promise.reject(new Error("boom")) : Promise.resolve([link("l", "s200", "s201")]),
    );
    const chunks = await fetchLinksFor({ listLinks }, ids(250));
    expect(chunks.map((c) => c.result.status)).toEqual(["rejected", "fulfilled"]);
  });
});

describe("fetchAllLinksFor", () => {
  it("merges chunks, keeping a link that spans two chunks once", async () => {
    const span = link("span", "s0", "s300");
    const listLinks = vi.fn(() => Promise.resolve([span]));
    expect(await fetchAllLinksFor({ listLinks }, ids(301))).toEqual([span]);
    expect(listLinks).toHaveBeenCalledTimes(2);
  });

  it("rejects when any chunk fails", async () => {
    const listLinks = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error("offline"));
    await expect(fetchAllLinksFor({ listLinks }, ids(201))).rejects.toThrow("offline");
  });
});

describe("fetchLinksFor on a missing bulk route", () => {
  it.each([404, 405])("rejects the chunk on a %i, with no other read", async (status) => {
    const err = new RouterHTTPError(status, "404 page not found");
    const listLinks = vi.fn(() => Promise.reject(err));
    const [chunk] = await fetchLinksFor({ listLinks }, ["a", "b"]);
    expect(chunk.result).toEqual({ status: "rejected", reason: err });
    expect(listLinks).toHaveBeenCalledTimes(1);
  });
});
