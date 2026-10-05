import { useCallback, useEffect, useState } from "react";
import type { Session } from "../../../router/types";

/** localStorage key: a JSON array of the session ids the user has already met. */
export const SEEN_KEY = "mads.sessionRail.seen";

function readSeen(): Set<string> | null {
  try {
    const raw = localStorage.getItem(SEEN_KEY);
    if (raw === null) return null;
    const ids: unknown = JSON.parse(raw);
    if (!Array.isArray(ids)) return null;
    return new Set(ids.filter((id): id is string => typeof id === "string"));
  } catch {
    return null;
  }
}

function writeSeen(seen: Set<string>): void {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify([...seen]));
  } catch {
    // Storage full or blocked: the highlight just won't survive a reload.
  }
}

/**
 * Which cards are new to the user. The rail stores the ids it has *seen*, not
 * the ones it hasn't, so a session that arrives from anywhere — an import, a
 * pipeline, a linked session — is unseen by default with no producer having
 * to flag it. A card is seen once it is hovered, focused or opened.
 *
 * With no stored state the first loaded list is seeded as seen, so upgrading
 * does not light up an entire existing rail. `loaded` says the list is real:
 * seeding from the empty pre-load list would make every session that then
 * arrives look new. Absent, a non-empty list is taken to be loaded.
 */
export function useUnseenSessions(
  sessions: Session[],
  selectedId: string | undefined,
  loaded: boolean = sessions.length > 0,
) {
  const [seen, setSeen] = useState<Set<string> | null>(readSeen);

  // Seed during render rather than in an effect, so the first paint never
  // shows the whole rail unseen for a frame.
  let current = seen;
  if (current === null && loaded) {
    current = new Set(sessions.map((s) => s.id));
    setSeen(current);
  }

  const markSeen = useCallback((id: string) => {
    setSeen((prev) => (prev === null || prev.has(id) ? prev : new Set(prev).add(id)));
  }, []);

  // Opening a card is at least as strong a signal as hovering it.
  useEffect(() => {
    if (selectedId) markSeen(selectedId);
  }, [selectedId, markSeen]);

  // Forget ids whose session is gone so the stored list can't grow forever. An
  // empty list is never pruned against: that is a failed refresh as often as
  // it is a rail genuinely emptied, and wiping the ids would relight every
  // card when the list comes back.
  useEffect(() => {
    if (seen === null || sessions.length === 0) return;
    const live = new Set(sessions.map((s) => s.id));
    if ([...seen].every((id) => live.has(id))) return;
    setSeen(new Set([...seen].filter((id) => live.has(id))));
  }, [seen, sessions]);

  useEffect(() => {
    if (current !== null) writeSeen(current);
  }, [current]);

  const isUnseen = (id: string) => current !== null && !current.has(id) && id !== selectedId;
  return { isUnseen, markSeen };
}
