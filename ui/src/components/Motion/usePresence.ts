import { useEffect, useState } from "react";

/** --dur-fast: a small floating thing (chip, pill) fading out. */
export const FADE_OUT_MS = 120;

/**
 * Keeps something rendered for `exitMs` after `show` goes false so it can
 * play an exit. `leaving` is true for that window.
 */
export function usePresence(show: boolean, exitMs: number): { present: boolean; leaving: boolean } {
  const [present, setPresent] = useState(show);
  if (show && !present) setPresent(true);
  useEffect(() => {
    if (show || !present) return;
    const t = window.setTimeout(() => setPresent(false), exitMs);
    return () => window.clearTimeout(t);
  }, [show, present, exitMs]);
  return { present: show || present, leaving: !show && present };
}
