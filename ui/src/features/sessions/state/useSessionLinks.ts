import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { FLAGS, subscribeFlags } from "../../../featureFlags";
import type { Link } from "../../../router/types";

// Link CRUD events are only server-scoped by SubjectSessionID (unlike
// SESSION_EVENT_TYPES' session.* events), so a rail-wide "did any link
// involving any visible session change" subscription must go unscoped (no
// sessionId param) with this explicit types list instead.
const LINK_EVENT_TYPES = ["link.created", "link.updated", "link.deleted"];

/**
 * Fetches every link touching a visible session and dedupes client-side.
 * No bulk `GET /links` route exists (docs/linked-sessions-mockup.html /
 * ui/src/features/sessions/HANDOFF.md decision D2) — only
 * `GET /sessions/{id}/links`, scoped to one session but returning links
 * where that session is on either side. This is an N+1 fetch by design,
 * not an oversight; flag if it becomes a perf problem at real session
 * counts.
 */
export function useSessionLinks(sessionIds: string[]): Link[] {
  const [links, setLinks] = useState<Link[]>([]);
  const mounted = useRef(true);
  const idsKey = [...sessionIds].sort().join(",");

  const refetch = useCallback(async () => {
    if (!FLAGS.linkedSessions || sessionIds.length === 0) {
      if (mounted.current) setLinks([]);
      return;
    }
    const results = await Promise.all(sessionIds.map((id) => router.getSessionLinks(id)));
    if (!mounted.current) return;
    const byId = new Map<string, Link>();
    for (const list of results) {
      for (const link of list) byId.set(link.id, link);
    }
    setLinks([...byId.values()]);
    // idsKey is the stable dependency; sessionIds itself is a new array reference every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [idsKey]);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribeEvents = subscribeToEvents({ types: LINK_EVENT_TYPES }, () => {
      void refetch();
    });
    const unsubscribeFlags = subscribeFlags(() => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribeEvents();
      unsubscribeFlags();
    };
  }, [refetch]);

  return links;
}
