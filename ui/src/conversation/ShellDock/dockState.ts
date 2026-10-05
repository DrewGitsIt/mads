import { useSyncExternalStore } from "react";

/**
 * Per-session view state of the shell dock (docs/SHELL-DOCK-DESIGN.md D4):
 * whether it is showing and how tall it is. It lives outside React so it
 * survives what unmounts the tab (SessionWorkspace is keyed by session id, so
 * a session switch rebuilds ConversationTab) and is written through to
 * localStorage so a window reload restores it. Whether a shell *exists* is
 * not stored here — the shell host is the source of truth (useLiveShells).
 */
export interface ShellDockState {
  open: boolean;
  height: number;
}

export const DEFAULT_DOCK_HEIGHT = 280;
const DEFAULT_STATE: ShellDockState = { open: false, height: DEFAULT_DOCK_HEIGHT };

const storageKey = (sessionId: string) => `mads.shellDock.${sessionId}`;

// One object per session, replaced (never mutated) on change, so the
// useSyncExternalStore snapshot is referentially stable between changes.
const cache = new Map<string, ShellDockState>();
const listeners = new Set<() => void>();

function read(sessionId: string): ShellDockState {
  try {
    const raw = window.localStorage.getItem(storageKey(sessionId));
    if (!raw) return DEFAULT_STATE;
    const parsed = JSON.parse(raw) as Partial<ShellDockState>;
    return {
      open: parsed.open === true,
      height:
        typeof parsed.height === "number" && Number.isFinite(parsed.height) && parsed.height > 0
          ? parsed.height
          : DEFAULT_DOCK_HEIGHT,
    };
  } catch {
    // Unreadable or storage-less (private mode): behave as never opened.
    return DEFAULT_STATE;
  }
}

export function getShellDockState(sessionId: string): ShellDockState {
  let state = cache.get(sessionId);
  if (!state) {
    state = read(sessionId);
    cache.set(sessionId, state);
  }
  return state;
}

export function setShellDockState(sessionId: string, patch: Partial<ShellDockState>): void {
  const current = getShellDockState(sessionId);
  const next = { ...current, ...patch };
  if (next.open === current.open && next.height === current.height) return;
  cache.set(sessionId, next);
  try {
    window.localStorage.setItem(storageKey(sessionId), JSON.stringify(next));
  } catch {
    // Quota or storage disabled: the in-memory state still serves this window.
  }
  listeners.forEach((listener) => listener());
}

/** Test seam: forgets the in-memory copy so the next read goes back to localStorage. */
export function resetShellDockCache(): void {
  cache.clear();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useShellDockState(sessionId: string): ShellDockState {
  return useSyncExternalStore(
    subscribe,
    () => getShellDockState(sessionId),
    () => DEFAULT_STATE,
  );
}
