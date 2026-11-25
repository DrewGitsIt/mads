import { useEffect, useRef } from "react";

export interface PollOptions {
  /** The first interval, and the one after a success while young. */
  every: number;
  /** The interval never grows past this. */
  max: number;
  /** After this long, successes wait twice as long too (a dialog left open). 0: never. */
  slowAfter?: number;
  /** Read once at the start, before the first wait. */
  immediate?: boolean;
}

/** Whether the page is on screen now. */
export const pageVisible = () => typeof document === "undefined" || document.visibilityState !== "hidden";

/**
 * Runs `read` repeatedly while `active` (S3): never while the page is
 * hidden (it resumes, with a read, when shown again), backing off on
 * failures (doubling up to `max`), and slowing down over time. `read`'s
 * promise is awaited, so reads never overlap.
 */
export function usePoll(read: () => Promise<unknown>, active: boolean, opts: PollOptions): void {
  const readRef = useRef(read);
  readRef.current = read;
  const { every, max, slowAfter = 0, immediate = false } = opts;
  useEffect(() => {
    if (!active) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let wait = every;
    let running = false;
    const started = Date.now();
    const schedule = () => {
      if (!live || !pageVisible()) return;
      clearTimeout(timer);
      timer = setTimeout(tick, wait);
    };
    const tick = async () => {
      if (!live || running || !pageVisible()) return;
      running = true;
      try {
        await readRef.current();
        const old = slowAfter > 0 && Date.now() - started > slowAfter;
        wait = old ? Math.min(max, Math.max(wait, every * 2)) : every;
      } catch {
        wait = Math.min(max, wait * 2);
      } finally {
        running = false;
      }
      schedule();
    };
    const onVisible = () => {
      if (!live) return;
      clearTimeout(timer);
      if (pageVisible()) void tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    if (immediate) void tick();
    else schedule();
    return () => {
      live = false;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [active, every, max, slowAfter, immediate]);
}
