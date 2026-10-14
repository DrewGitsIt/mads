// "New since you looked" (docs/CHANGES-DESIGN.md): a light per-file marker,
// persisted per session in localStorage as { [absolute path]: ISO time }.
// A file is new when it has no entry, or its last_changed is newer than the
// entry. Selecting a file in the Changes tab marks it seen.
import { useCallback, useEffect, useState } from "react";
import type { ChangedFile } from "../router/types";

export type SeenMap = Record<string, string>;

export function seenKey(sessionId: string): string {
  return `mads.changes.seen.${sessionId}`;
}

export function loadSeen(sessionId: string): SeenMap {
  try {
    const raw = localStorage.getItem(seenKey(sessionId));
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as SeenMap) : {};
  } catch {
    return {};
  }
}

export function saveSeen(sessionId: string, seen: SeenMap): void {
  try {
    localStorage.setItem(seenKey(sessionId), JSON.stringify(seen));
  } catch {
    // Storage full or unavailable: the marker is a convenience, not state.
  }
}

export function isUnseen(seen: SeenMap, f: Pick<ChangedFile, "path" | "last_changed">): boolean {
  const at = seen[f.path];
  if (!at) return true;
  const changed = Date.parse(f.last_changed);
  const looked = Date.parse(at);
  if (Number.isNaN(changed) || Number.isNaN(looked)) return false;
  return changed > looked;
}

/**
 * The map after looking at `f`. Records the file's own last_changed (or now
 * when it has none), so a later change compares against server time and a
 * skewed client clock can't hide it.
 */
export function withSeen(seen: SeenMap, f: Pick<ChangedFile, "path" | "last_changed">, now = new Date()): SeenMap {
  const stamp = Number.isNaN(Date.parse(f.last_changed)) ? now.toISOString() : f.last_changed;
  if (seen[f.path] === stamp) return seen;
  return { ...seen, [f.path]: stamp };
}

// Same-window sync: the tab and the rail each hold the hook, and the
// browser's "storage" event only fires in *other* windows.
const SEEN_EVENT = "mads:changes-seen";

/** Seen-state for one session, shared live between the Changes tab and the rail. */
export function useSeenState(sessionId: string): {
  seen: SeenMap;
  isUnseen: (f: Pick<ChangedFile, "path" | "last_changed">) => boolean;
  markSeen: (f: Pick<ChangedFile, "path" | "last_changed">) => void;
} {
  const [seen, setSeen] = useState<SeenMap>(() => loadSeen(sessionId));

  useEffect(() => {
    setSeen(loadSeen(sessionId));
    function reload(e: Event) {
      if (e instanceof StorageEvent ? e.key === seenKey(sessionId) : (e as CustomEvent<string>).detail === sessionId) {
        setSeen(loadSeen(sessionId));
      }
    }
    window.addEventListener(SEEN_EVENT, reload);
    window.addEventListener("storage", reload);
    return () => {
      window.removeEventListener(SEEN_EVENT, reload);
      window.removeEventListener("storage", reload);
    };
  }, [sessionId]);

  const markSeen = useCallback(
    (f: Pick<ChangedFile, "path" | "last_changed">) => {
      const current = loadSeen(sessionId);
      const next = withSeen(current, f);
      if (next === current) return;
      saveSeen(sessionId, next);
      setSeen(next);
      window.dispatchEvent(new CustomEvent(SEEN_EVENT, { detail: sessionId }));
    },
    [sessionId],
  );

  const check = useCallback((f: Pick<ChangedFile, "path" | "last_changed">) => isUnseen(seen, f), [seen]);

  return { seen, isUnseen: check, markSeen };
}
