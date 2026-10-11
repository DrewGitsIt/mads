// Cross-tree signals for jumping between top-level surfaces. App.tsx owns
// which surface shows, but the callers (the shell dock's soft-limit notice,
// the Settings shell inventory) sit several layers down, so — like
// shellToggleSignal — they fire a plain window Event that App listens for.
export const OPEN_SETTINGS_EVENT = "mads:open-settings";
export const OPEN_SESSION_EVENT = "mads:open-session";

/**
 * Show Settings scrolled to `section`. The section rides in the URL hash, which
 * SettingsSurface already reads on mount to scroll to a deep-linked section.
 */
export function requestOpenSettings(section: string): void {
  window.history.replaceState(null, "", `#${section}`);
  window.dispatchEvent(new CustomEvent(OPEN_SETTINGS_EVENT, { detail: { section } }));
}

/** Show the Sessions surface with `sessionId` selected. */
export function requestOpenSession(sessionId: string): void {
  window.dispatchEvent(new CustomEvent(OPEN_SESSION_EVENT, { detail: { sessionId } }));
}

/**
 * A surface with something to lose (Settings' unsaved draft) registers one of
 * these while mounted. Return true to hold the leave — and later call
 * `proceed` if the user decides to go after all — or false to let it through.
 */
export type LeaveGuard = (proceed: () => void) => boolean;

let leaveGuard: LeaveGuard | null = null;

/** Installs `guard`; the returned function removes it (if it's still the one installed). */
export function registerLeaveGuard(guard: LeaveGuard): () => void {
  leaveGuard = guard;
  return () => {
    if (leaveGuard === guard) leaveGuard = null;
  };
}

/** Runs `proceed` — App's surface switch — unless the current surface's guard holds it. */
export function guardLeave(proceed: () => void): void {
  if (leaveGuard?.(proceed)) return;
  proceed();
}
