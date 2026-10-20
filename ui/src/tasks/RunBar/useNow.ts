import { useEffect, useReducer } from "react";

/** `Date.now()` as of this render; while `ticking`, re-renders each second. */
export function useNow(ticking: boolean): number {
  const [, tick] = useReducer((x: number) => x + 1, 0);
  useEffect(() => {
    if (!ticking) return;
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [ticking]);
  return Date.now();
}
