/** Trailing quiet window before a requested run fires. */
export const COALESCE_WAIT_MS = 50;
/** Upper bound on how long a continuous stream of requests can defer a run. */
export const COALESCE_MAX_WAIT_MS = 500;

export interface Coalescer {
  /** Something changed: run once after `waitMs` of quiet, never later than `maxWaitMs` after the first request in this window. */
  request(): void;
  /** Run now (still single-flight). */
  now(): void;
  /** Stop: clear timers and any pending follow-up. A run already in flight finishes but triggers no follow-up. */
  cancel(): void;
}

/**
 * Collapses a burst of "something changed" signals into few runs of `run`:
 * a trailing debounce capped by a max wait (the same shape as
 * useSessionLinks' schedule/flush), plus single-flight — never two runs at
 * once, and a request that lands mid-run buys exactly one more run after it
 * settles, so a response already in flight can't be the last word. Rejections
 * from `run` are swallowed here; `run` reports its own errors.
 */
export function coalesce(
  run: () => Promise<unknown>,
  opts: { waitMs?: number; maxWaitMs?: number } = {},
): Coalescer {
  const waitMs = opts.waitMs ?? COALESCE_WAIT_MS;
  const maxWaitMs = opts.maxWaitMs ?? COALESCE_MAX_WAIT_MS;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let windowStart: number | null = null;
  let inFlight = false;
  let dirty = false;

  function clearTimer(): void {
    if (timer) clearTimeout(timer);
    timer = undefined;
    windowStart = null;
  }

  function settle(): void {
    inFlight = false;
    if (!dirty) return;
    dirty = false;
    start();
  }

  function start(): void {
    clearTimer();
    if (inFlight) {
      dirty = true;
      return;
    }
    inFlight = true;
    let p: Promise<unknown>;
    try {
      p = run();
    } catch (err) {
      p = Promise.reject(err);
    }
    void Promise.resolve(p).then(settle, settle);
  }

  return {
    request() {
      // Mid-run, the follow-up after settle covers this request already.
      if (inFlight) {
        dirty = true;
        return;
      }
      const t = Date.now();
      if (windowStart === null) windowStart = t;
      if (timer) clearTimeout(timer);
      const delay = Math.max(0, Math.min(waitMs, windowStart + maxWaitMs - t));
      timer = setTimeout(start, delay);
    },
    now: start,
    cancel() {
      clearTimer();
      dirty = false;
    },
  };
}
