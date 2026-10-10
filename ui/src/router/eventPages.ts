import type { RouterClient } from "./client";
import type { Envelope } from "./types";

// services/data/internal/api/events.go defaultBatchLimit — a full page means
// there may be more.
export const EVENTS_BATCH_LIMIT = 500;
// A backstop against a server that ignores `since`: 50 pages is 25k events,
// far past any real history.
export const MAX_EVENT_PAGES = 50;

/**
 * Every event matching `filter`, oldest first. GET /events answers at most
 * EVENTS_BATCH_LIMIT envelopes per call — the oldest ones — so a single read
 * of a long session's history silently stops partway: everything after the
 * first page is simply missing. This reads page after page from the last seq
 * until a short page says there are no more.
 */
export async function getAllEvents(
  client: Pick<RouterClient, "getEvents">,
  filter: { sessionId: string; since?: number; types?: string[] },
): Promise<Envelope[]> {
  const all: Envelope[] = [];
  let cursor = filter.since;
  for (let page = 0; page < MAX_EVENT_PAGES; page++) {
    const batch = await client.getEvents({ ...filter, since: cursor });
    all.push(...batch);
    if (batch.length < EVENTS_BATCH_LIMIT) break;
    cursor = batch[batch.length - 1].seq;
  }
  return all;
}
