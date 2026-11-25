import { useEffect, useState } from "react";

/**
 * Seconds left before Try again may be pressed: counts down from `after`
 * each time `key` changes (a new failure), 0 once it may.
 */
export function useRetryWait(key: unknown, after: number): number {
  const [left, setLeft] = useState(after);
  useEffect(() => {
    setLeft(after);
    if (after <= 0) return;
    const until = Date.now() + after * 1000;
    const t = setInterval(() => {
      const s = Math.max(0, Math.ceil((until - Date.now()) / 1000));
      setLeft(s);
      if (s === 0) clearInterval(t);
    }, 250);
    return () => clearInterval(t);
  }, [key, after]);
  return left;
}
