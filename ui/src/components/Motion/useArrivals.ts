import { useEffect, useRef } from "react";

/**
 * Which keys arrived after this component mounted — the ones worth an
 * entrance. Everything present at mount is history and never animates, and
 * a key stops counting as arriving once it has been rendered, so content
 * that remounts (a group reopened) doesn't replay its entrance.
 */
export function useArrivals(keys: readonly string[], enabled = true): (key: string) => boolean {
  const seen = useRef<Set<string> | null>(null);
  if (seen.current === null) seen.current = new Set(keys);
  // After commit, not during render: StrictMode's double render must read
  // the same answer twice.
  useEffect(() => {
    for (const k of keys) seen.current!.add(k);
  });
  return (key) => enabled && !seen.current!.has(key);
}
