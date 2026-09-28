import { useSyncExternalStore } from "react";

/**
 * The app's one relative-timestamp formatter, plus the single shared tick that
 * re-renders every label using it.
 *
 * Shape (mockup docs/mockups/sessions-ux-v2.html §3): sub-minute "now"; under
 * an hour "14m"; today "13:02"; this week "Fri 09:14"; older "24 Sep". The
 * bucketed form is deliberately lossy, so every caller should also hand
 * formatAbsoluteStamp() to a `title` so the exact instant stays recoverable.
 */

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const WEEK = 7 * 24 * HOUR;

/** Every relative label in the window re-renders off this one interval. */
const TICK_MS = 30_000;
/** Elapsed counters need seconds ("31s"), so they ride a second, faster clock. */
const ELAPSED_TICK_MS = 1_000;

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function isSameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

/** h23 rather than hour12:false — en-US renders midnight as "24:00" under h24. */
function hhmm(d: Date): string {
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
}

/** The calendar half of the scale, shared by both public formatters. */
function calendarStamp(d: Date, now: Date, todayPrefix: string): string {
  if (isSameDay(d, now)) return `${todayPrefix}${hhmm(d)}`;
  if (now.getTime() - d.getTime() < WEEK) {
    return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${hhmm(d)}`;
  }
  return `${d.getDate()} ${d.toLocaleDateString(undefined, { month: "short" })}`;
}

/** "now" · "14m" · "13:02" · "Fri 09:14" · "24 Sep". Empty for an unparseable stamp. */
export function formatRelativeStamp(iso: string | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return "";
  const diff = now - t;
  // A stamp in the future (clock skew between router and browser) reads "now"
  // rather than a negative minute count.
  if (diff < MINUTE) return "now";
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)}m`;
  return calendarStamp(new Date(t), new Date(now), "");
}

/** The spelled-out form for a card footer: "today 13:02" · "Fri 09:14" · "24 Sep". */
export function formatCalendarStamp(iso: string | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return "";
  return calendarStamp(new Date(t), new Date(now), "today ");
}

/** How long a live state has been running: "4s" · "2m" · "1h". Empty below a second. */
export function formatElapsedStamp(iso: string | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return "";
  const diff = now - t;
  if (diff < 1000) return "";
  if (diff < MINUTE) return `${Math.floor(diff / 1000)}s`;
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)}m`;
  return `${Math.floor(diff / HOUR)}h`;
}

/** The whole truth, for a `title`: local "2026-09-24 13:02:47". */
export function formatAbsoluteStamp(iso: string | undefined): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return "";
  const d = new Date(t);
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  );
}

/** One interval per cadence for the whole window, however many labels are mounted. */
function makeClock(intervalMs: number) {
  let timer: ReturnType<typeof setInterval> | undefined;
  let nowMs = Date.now();
  const listeners = new Set<() => void>();

  function subscribe(onStoreChange: () => void): () => void {
    listeners.add(onStoreChange);
    if (timer === undefined) {
      nowMs = Date.now();
      timer = setInterval(() => {
        nowMs = Date.now();
        for (const listener of listeners) listener();
      }, intervalMs);
    }
    return () => {
      listeners.delete(onStoreChange);
      if (listeners.size === 0 && timer !== undefined) {
        clearInterval(timer);
        timer = undefined;
      }
    };
  }

  return { subscribe, getSnapshot: () => nowMs };
}

// A rail of 40 cards must not mean 40 timers — and the seconds clock only
// exists while something is actually counting, because nothing subscribes to
// it unless a card is mid-turn or parked on an approval.
const relativeClock = makeClock(TICK_MS);
const elapsedClock = makeClock(ELAPSED_TICK_MS);

/** Current time, re-rendering the caller on the shared 30s tick. */
export function useRelativeNow(): number {
  return useSyncExternalStore(
    relativeClock.subscribe,
    relativeClock.getSnapshot,
    relativeClock.getSnapshot,
  );
}

/** Current time on the shared 1s tick — for a live elapsed counter. */
export function useElapsedNow(): number {
  return useSyncExternalStore(
    elapsedClock.subscribe,
    elapsedClock.getSnapshot,
    elapsedClock.getSnapshot,
  );
}
