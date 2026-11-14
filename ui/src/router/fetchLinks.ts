import { MAX_LINK_SESSION_IDS, RouterHTTPError, type RouterClient } from "./client";
import type { Link } from "./types";

type LinkReader = Pick<RouterClient, "listLinks" | "getSessionLinks">;

/** One chunk's read: the ids it asked about and how it settled. */
export interface LinkChunk {
  ids: string[];
  result: PromiseSettledResult<Link[]>;
}

// ui/dist is rebuilt separately from the long-running Router and Data
// Service (the same skew events.ts's probeRejectedTypes handles), so the
// bulk route can be missing: 404 on an older Router or an older Data Service
// behind a new one, 405 where only POST /links is routed. The first such
// answer flips this for the page's life; every later read goes per id.
let bulkUnsupported = false;

/** Forgets a missing bulk route. Tests only. */
export function resetBulkLinksSupport(): void {
  bulkUnsupported = false;
}

const routeMissing = (err: unknown) => err instanceof RouterHTTPError && (err.status === 404 || err.status === 405);

/**
 * Every link touching `sessionIds`, as one listLinks request per
 * MAX_LINK_SESSION_IDS distinct ids (one total for a normal rail). Each
 * chunk settles on its own, so a caller can keep its cache for the ids of a
 * failed chunk. A link between ids in different chunks comes back in both.
 * No ids, no requests. Against a server without the bulk route, a chunk is
 * read with one getSessionLinks per id instead, and fulfills only if every
 * id did.
 */
export async function fetchLinksFor(client: LinkReader, sessionIds: Iterable<string>): Promise<LinkChunk[]> {
  const ids = [...new Set(sessionIds)];
  const chunks: string[][] = [];
  for (let i = 0; i < ids.length; i += MAX_LINK_SESSION_IDS) chunks.push(ids.slice(i, i + MAX_LINK_SESSION_IDS));
  const results = await Promise.allSettled(chunks.map((chunk) => readChunk(client, chunk)));
  return chunks.map((chunk, i) => ({ ids: chunk, result: results[i] }));
}

async function readChunk(client: LinkReader, ids: string[]): Promise<Link[]> {
  if (!bulkUnsupported) {
    try {
      return await client.listLinks(ids);
    } catch (err) {
      if (!routeMissing(err)) throw err;
      bulkUnsupported = true;
    }
  }
  const results = await Promise.allSettled(ids.map((id) => client.getSessionLinks(id)));
  const byId = new Map<string, Link>();
  for (const result of results) {
    if (result.status === "rejected") throw result.reason;
    for (const link of result.value) byId.set(link.id, link);
  }
  return [...byId.values()];
}

/** fetchLinksFor's links, deduped by id; rejects if any chunk failed. */
export async function fetchAllLinksFor(client: LinkReader, sessionIds: Iterable<string>): Promise<Link[]> {
  const byId = new Map<string, Link>();
  for (const { result } of await fetchLinksFor(client, sessionIds)) {
    if (result.status === "rejected") throw result.reason;
    for (const link of result.value) byId.set(link.id, link);
  }
  return [...byId.values()];
}
