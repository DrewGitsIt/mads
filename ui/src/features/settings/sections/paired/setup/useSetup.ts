import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ROUTER_BASE } from "../../../../../router/client";
import { FETCH_TIMEOUT_MS } from "../../../../../router/devices";
import { pageVisible, usePoll } from "../usePoll";
import { devicesApi, DevicesError, joinRetry, selfStore, type DiscoveredList, type JoinStatus } from "../../../../../router/devices";

/** How often an open, on-screen Add devices renews this device's setup window (the Router's own timer is 10 minutes, which is also how long a hidden page keeps it). */
export const RENEW_MS = 60_000;
/** How often the found list is read while Add devices is open (the search stops 30 s after the last read). */
export const DISCOVERED_POLL_MS = 2_000;
/** How often a found device's pairing is read while it's waiting on the other screen. */
export const JOIN_POLL_MS = 2_000;

export type SetupWindow =
  | { state: "opening" }
  | { state: "open"; until?: string }
  | { state: "closed"; reason?: string }
  | { state: "error"; error: string };

/**
 * This device's setup window (U4), one per page whoever asks for it: open
 * while some Add devices dialog holds it, renewed every minute while the
 * page is on screen, closed otherwise. Every open, renewal and close
 * goes through one queue, in order, so a quick close-then-open (React's
 * StrictMode, "Open again", a dialog closed and opened again) never lands
 * out of order. A hidden page only stops renewing, so the window lasts as
 * long as an unrenewed one (the Router's 10 minutes) and a pairing under
 * way survives mads being covered; shown again, it renews at once. Leaving
 * the page sends a close that outlives it. A renewal answered 409
 * window_closed (30 minutes, or it closed itself after abandoned
 * pairings) holds it closed with the reason until reopen().
 */
let holders = 0;
let opened = false; // what this page last asked the Router for
let heldClosed: string | undefined | null = null; // non-null: closed by the Router, with its reason
let queue: Promise<unknown> = Promise.resolve();
let renewTimer: ReturnType<typeof setInterval> | undefined;
let syncPending = false;
let current: SetupWindow = { state: "opening" };
const listeners = new Set<() => void>();
let pageHooks = false;

function emit(next: SetupWindow) {
  current = next;
  for (const l of listeners) l();
}

function enqueue(f: () => Promise<unknown>) {
  queue = queue.then(f, f).catch(() => {});
}

function closedBy(err: unknown) {
  if (err instanceof DevicesError && err.code === "window_closed") {
    heldClosed = typeof err.extra.reason === "string" ? err.extra.reason : undefined;
    opened = false;
    stopRenewing();
    emit({ state: "closed", reason: heldClosed });
  } else {
    emit({ state: "error", error: err instanceof Error ? err.message : String(err) });
  }
}

function stopRenewing() {
  if (renewTimer) clearInterval(renewTimer);
  renewTimer = undefined;
}

function renew() {
  if (!opened) return;
  enqueue(() => devicesApi.discoverable(true, true).then((r) => opened && emit({ state: "open", until: r.until }), closedBy));
}

/** Shown again: renew now (it may have lapsed while hidden: then it shows closed, with Open again). */
function onVisibility() {
  if (pageVisible()) renew();
}

function sync() {
  syncPending = false;
  const want = holders > 0 && heldClosed === null;
  if (want && !opened) {
    opened = true;
    if (current.state !== "open") emit({ state: "opening" });
    enqueue(() =>
      devicesApi.discoverable(true).then((r) => {
        if (!opened) return;
        emit({ state: "open", until: r.until });
        void selfStore.refresh();
      }, closedBy),
    );
    stopRenewing();
    renewTimer = setInterval(() => {
      if (pageVisible()) renew();
    }, RENEW_MS);
  } else if (!want && opened) {
    opened = false;
    stopRenewing();
    // Closing also ends the search and the window's pending discovered requests.
    enqueue(() => devicesApi.discoverable(false).then(() => void selfStore.refresh(), () => {}));
  }
}

/** Coalesces this tick's holds and releases: StrictMode's mount, unmount, mount is one hold. */
function scheduleSync() {
  if (syncPending) return;
  syncPending = true;
  queueMicrotask(sync);
}

/** Leaving the page: a close that outlives it (Electron adds the bearer below the page, keepalive or not). */
function closeOnLeave() {
  if (!opened) return;
  opened = false;
  stopRenewing();
  try {
    void fetch(`${ROUTER_BASE}/devices/discoverable`, {
      method: "POST",
      keepalive: true,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ on: false }),
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
    }).catch(() => {});
  } catch {
    // The page is going anyway; the Router's own timer closes it.
  }
}

function hookPage() {
  if (pageHooks || typeof window === "undefined") return;
  pageHooks = true;
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pagehide", closeOnLeave);
}

function hold(): () => void {
  hookPage();
  holders++;
  scheduleSync();
  return () => {
    holders--;
    scheduleSync();
  };
}

function reopenWindow() {
  heldClosed = null;
  opened = false;
  scheduleSync();
}

export function resetSetupWindowForTests(): void {
  holders = 0;
  opened = false;
  heldClosed = null;
  stopRenewing();
  queue = Promise.resolve();
  syncPending = false;
  current = { state: "opening" };
  listeners.clear();
  if (pageHooks) {
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("pagehide", closeOnLeave);
    pageHooks = false;
  }
}

/** This page's setup window, held open while `active` (the dialog is open). */
export function useSetupWindow(active: boolean): [SetupWindow, () => void] {
  const win = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
  useEffect(() => {
    if (!active) return;
    return hold();
  }, [active]);
  return [win, reopenWindow];
}

/** The found list, read every DISCOVERED_POLL_MS while `active` and the page is on screen (backing off on errors, slower after two minutes). */
export function useDiscovered(active: boolean): { list?: DiscoveredList; error?: { message: string; code?: string; status?: number } } {
  const [list, setList] = useState<DiscoveredList>();
  const [error, setError] = useState<{ message: string; code?: string; status?: number }>();
  usePoll(
    () =>
      devicesApi.discovered().then(
        (l) => {
          setList(l);
          setError(undefined);
        },
        (err: unknown) => {
          setError(
            err instanceof DevicesError
              ? { message: err.message, code: err.code, status: err.status }
              : { message: err instanceof Error ? err.message : String(err) },
          );
          throw err;
        },
      ),
    active,
    { every: DISCOVERED_POLL_MS, max: 15_000, slowAfter: 120_000, immediate: true },
  );
  return { list, error };
}

/**
 * A found device's pairing, from Add to its answer: request() sends the
 * discovered request; while it waits on the host, it's read every
 * JOIN_POLL_MS; confirm() is the joiner's "Did it show this number?".
 */
export function useFoundJoin(fingerprint: string) {
  const [status, setStatus] = useState<JoinStatus>();
  // retryable/after: the same request may be sent again (F6 R7).
  const [error, setError] = useState<{ code?: string; message: string; retryable?: boolean; after?: number }>();
  const [busy, setBusy] = useState(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const request = useCallback(async () => {
    setBusy(true);
    setError(undefined);
    try {
      const st = await devicesApi.requestDiscovered(fingerprint);
      if (live.current) setStatus(st);
    } catch (err) {
      if (live.current) {
        const retry = joinRetry(err);
        setError({ code: err instanceof DevicesError ? err.code : undefined, message: err instanceof Error ? err.message : String(err), ...retry });
      }
    } finally {
      if (live.current) setBusy(false);
    }
  }, [fingerprint]);

  const confirm = useCallback(
    async (match: boolean) => {
      if (!status) return;
      setBusy(true);
      setError(undefined);
      try {
        const st = await devicesApi.confirmJoin(status.id, match);
        if (live.current) setStatus(st);
      } catch (err) {
        if (live.current) setError({ code: err instanceof DevicesError ? err.code : undefined, message: err instanceof Error ? err.message : String(err) });
      } finally {
        if (live.current) setBusy(false);
      }
    },
    [status],
  );

  const id = status?.state === "pending" ? status.id : undefined;
  usePoll(
    () =>
      devicesApi.joinStatus(id!).then((st) => {
        if (live.current) setStatus(st);
      }),
    Boolean(id),
    { every: JOIN_POLL_MS, max: 15_000 },
  );

  return { status, error, busy, request, confirm, reset: () => (setStatus(undefined), setError(undefined)) };
}
