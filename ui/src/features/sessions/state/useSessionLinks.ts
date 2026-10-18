import { useEffect, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { FLAGS, subscribeFlags } from "../../../featureFlags";
import type { Envelope, Link } from "../../../router/types";

// Link CRUD events are only server-scoped by SubjectSessionID (unlike
// SESSION_EVENT_TYPES' session.* events), so a rail-wide "did any link
// involving any visible session change" subscription must go unscoped (no
// sessionId param) with this explicit types list instead.
const LINK_EVENT_TYPES = ["link.created", "link.updated", "link.deleted"];

/** Trailing quiet window before pending link refetches flush. */
export const LINK_REFETCH_COALESCE_MS = 100;
/** Upper bound on how long a continuous event stream can defer a flush. */
export const LINK_REFETCH_MAX_WAIT_MS = 500;

/**
 * Keeps every link touching a visible session. No bulk `GET /links` route
 * exists (docs/linked-sessions-mockup.html / ui/src/features/sessions/HANDOFF.md
 * decision D2) — only `GET /sessions/{id}/links`, which returns links where
 * that session is on either side. So for a link event between G and S,
 * refetching G and S, dropping every cached link touching either, and
 * inserting the results is exactly a full refetch — and link events fire
 * constantly (pipeline start/stop hand-raiser links, leader tool loads), so
 * refetching all N sessions per event is not affordable.
 *
 * Strategy: events add their in-set endpoints to a pending set, flushed
 * after a trailing quiet window (capped by a max wait) as one fetch per id,
 * one merge, one setLinks. Single-flight: events during a flight wait for
 * it to land, so responses apply in request order. A generation counter
 * drops results from before unmount or a flag flip; results for ids that
 * left the set mid-flight are skipped. Full refetch only on mount, flag-on,
 * and an unreadable event payload. Fetch errors keep the cache as is.
 */
export function useSessionLinks(sessionIds: string[]): Link[] {
  const [links, setLinks] = useState<Link[]>([]);
  const [sync] = useState(() => new LinkSync(setLinks));
  const idsKey = [...sessionIds].sort().join(",");

  // Declared before the lifecycle effect so start() sees the initial ids.
  useEffect(() => {
    sync.setIds(new Set(idsKey ? idsKey.split(",") : []));
  }, [sync, idsKey]);

  useEffect(() => {
    sync.start();
    const unsubscribeEvents = subscribeToEvents({ types: LINK_EVENT_TYPES }, (env) => sync.onEvent(env));
    const unsubscribeFlags = subscribeFlags(() => sync.onFlags());
    return () => {
      unsubscribeEvents();
      unsubscribeFlags();
      sync.stop();
    };
  }, [sync]);

  return links;
}

/** A link event's two endpoints, or null when the payload doesn't say. */
function linkEndpoints(env: Envelope): [string, string] | null {
  let payload: unknown = env.payload;
  if (typeof payload === "string") {
    try {
      payload = JSON.parse(payload);
    } catch {
      return null;
    }
  }
  if (!payload || typeof payload !== "object") return null;
  const { granting_session_id: g, subject_session_id: s } = payload as Record<string, unknown>;
  if (typeof g !== "string" || !g || typeof s !== "string" || !s) return null;
  return [g, s];
}

const touches = (link: Link, ids: Set<string>) =>
  ids.has(link.granting_session_id) || ids.has(link.subject_session_id);

class LinkSync {
  private ids = new Set<string>();
  private cache = new Map<string, Link>();
  private pendingIds = new Set<string>();
  private pendingFull = false;
  private inFlight = false;
  private generation = 0;
  private active = false;
  private enabled = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private windowStart: number | null = null;

  private readonly publish: (links: Link[]) => void;

  constructor(publish: (links: Link[]) => void) {
    this.publish = publish;
  }

  start(): void {
    this.active = true;
    this.generation++;
    this.enabled = FLAGS.linkedSessions;
    if (this.enabled) {
      this.pendingFull = true;
      this.flush();
    } else {
      this.clearCache();
    }
  }

  stop(): void {
    this.active = false;
    this.generation++;
    this.inFlight = false;
    this.clearPending();
  }

  // Never a full refetch: added ids are fetched, and cached links that no
  // longer touch any visible id are dropped. A link between a kept and a
  // removed session stays — the kept session's GET returns it too.
  setIds(next: Set<string>): void {
    const added = [...next].filter((id) => !this.ids.has(id));
    this.ids = next;
    for (const id of this.pendingIds) if (!next.has(id)) this.pendingIds.delete(id);
    let pruned = false;
    for (const [id, link] of this.cache) {
      if (!touches(link, next)) {
        this.cache.delete(id);
        pruned = true;
      }
    }
    if (pruned) this.emit();
    if (!this.active || !this.enabled || added.length === 0) return;
    for (const id of added) this.pendingIds.add(id);
    this.flush();
  }

  onFlags(): void {
    if (!this.active || FLAGS.linkedSessions === this.enabled) return;
    this.enabled = FLAGS.linkedSessions;
    // Supersede anything in flight: its result predates the flip.
    this.generation++;
    this.inFlight = false;
    this.clearPending();
    if (this.enabled) {
      this.pendingFull = true;
      this.flush();
    } else {
      this.clearCache();
    }
  }

  onEvent(env: Envelope): void {
    if (!this.active || !this.enabled) return;
    const endpoints = linkEndpoints(env);
    if (!endpoints) {
      this.pendingFull = true;
    } else {
      const inSet = endpoints.filter((id) => this.ids.has(id));
      if (inSet.length === 0) return;
      for (const id of inSet) this.pendingIds.add(id);
    }
    this.schedule();
  }

  private schedule(): void {
    const now = Date.now();
    if (this.windowStart === null) this.windowStart = now;
    if (this.timer) clearTimeout(this.timer);
    const delay = Math.max(
      0,
      Math.min(LINK_REFETCH_COALESCE_MS, this.windowStart + LINK_REFETCH_MAX_WAIT_MS - now),
    );
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.windowStart = null;
      this.flush();
    }, delay);
  }

  // Takes everything pending as one batch. While a batch is in flight this
  // is a no-op; the landing batch flushes whatever accumulated meanwhile.
  private flush(): void {
    if (!this.active || !this.enabled || this.inFlight) return;
    // A full refetch supersedes any pending scoped ids.
    const targets = this.pendingFull ? [...this.ids] : [...this.pendingIds].filter((id) => this.ids.has(id));
    this.clearPending();
    if (targets.length === 0) return;
    this.inFlight = true;
    const generation = this.generation;
    void Promise.allSettled(targets.map((id) => router.getSessionLinks(id))).then((results) => {
      if (generation !== this.generation) return;
      this.inFlight = false;
      this.apply(targets, results);
      if ((this.pendingFull || this.pendingIds.size > 0) && !this.timer) this.flush();
    });
  }

  private apply(targets: string[], results: PromiseSettledResult<Link[]>[]): void {
    const fetched = new Set<string>();
    const incoming: Link[] = [];
    targets.forEach((id, i) => {
      const result = results[i];
      // A failed fetch leaves that id's cached links alone; an id that left
      // the set mid-flight was already pruned by setIds.
      if (result.status !== "fulfilled" || !this.ids.has(id)) return;
      fetched.add(id);
      incoming.push(...result.value);
    });
    if (fetched.size === 0) return;
    for (const [id, link] of this.cache) if (touches(link, fetched)) this.cache.delete(id);
    for (const link of incoming) this.cache.set(link.id, link);
    this.emit();
  }

  private clearPending(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    this.windowStart = null;
    this.pendingIds.clear();
    this.pendingFull = false;
  }

  private clearCache(): void {
    if (this.cache.size === 0) return;
    this.cache.clear();
    this.emit();
  }

  private emit(): void {
    this.publish([...this.cache.values()]);
  }
}
