import { MAX_LINK_SESSION_IDS, type RouterClient } from "./client";
import type { Link } from "./types";

type LinkReader = Pick<RouterClient, "listLinks">;

/** One chunk's read: the ids it asked about and how it settled. */
export interface LinkChunk {
  ids: string[];
  result: PromiseSettledResult<Link[]>;
}

/**
 * Every link touching `sessionIds`, as one listLinks request per
 * MAX_LINK_SESSION_IDS distinct ids (one total for a normal rail). Each
 * chunk settles on its own, so a caller can keep its cache for the ids of a
 * failed chunk. A link between ids in different chunks comes back in both.
 * No ids, no requests.
 */
export async function fetchLinksFor(client: LinkReader, sessionIds: Iterable<string>): Promise<LinkChunk[]> {
  const ids = [...new Set(sessionIds)];
  const chunks: string[][] = [];
  for (let i = 0; i < ids.length; i += MAX_LINK_SESSION_IDS) chunks.push(ids.slice(i, i + MAX_LINK_SESSION_IDS));
  const results = await Promise.allSettled(chunks.map((chunk) => client.listLinks(chunk)));
  return chunks.map((chunk, i) => ({ ids: chunk, result: results[i] }));
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
