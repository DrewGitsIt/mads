import type { Session } from "./router/types";

// Cross-tree signals for jumping between top-level surfaces. App.tsx owns
// which surface shows, but the callers (the shell dock's soft-limit notice,
// the Settings shell inventory) sit several layers down, so — like
// shellToggleSignal — they fire a plain window Event that App listens for.
export const OPEN_SETTINGS_EVENT = "mads:open-settings";
export const OPEN_SESSION_EVENT = "mads:open-session";
export const OPEN_LIBRARY_EVENT = "mads:open-library";

/**
 * Show Settings scrolled to `section`. The section rides in the URL hash, which
 * SettingsSurface already reads on mount to scroll to a deep-linked section.
 */
export function requestOpenSettings(section: string): void {
  window.history.replaceState(null, "", `#${section}`);
  window.dispatchEvent(new CustomEvent(OPEN_SETTINGS_EVENT, { detail: { section } }));
}

/** Show the Library on `shelf` (e.g. "workspaces"). */
export function requestOpenLibrary(shelf: string): void {
  window.dispatchEvent(new CustomEvent(OPEN_LIBRARY_EVENT, { detail: { shelf } }));
}

// The shelf the next Library mount should open on. App sets it only once the
// leave has gone ahead (a held leave mustn't leave a stale request behind);
// LibrarySurface reads it on mount and then clears it.
let requestedLibraryShelf: string | null = null;

export function setRequestedLibraryShelf(shelf: string | null): void {
  requestedLibraryShelf = shelf;
}

export function requestedShelf(): string | null {
  return requestedLibraryShelf;
}

/** Show the Sessions surface with `sessionId` selected. */
export function requestOpenSession(sessionId: string): void {
  window.dispatchEvent(new CustomEvent(OPEN_SESSION_EVENT, { detail: { sessionId } }));
}

export const OPEN_CREATED_SESSION_EVENT = "mads:open-created-session";

/** What a just-created session needs to open the way the Launchpad opens one. */
export interface CreatedSessionDetail {
  session: Session;
  /** The goal its composer sends as the opening message. */
  goal: string;
}

/**
 * Open a session that was just created (the composer's /new … ⌘↵): the
 * Sessions surface selects it exactly as it does after a Launchpad start —
 * provisioning strip, then the goal auto-sent from its composer.
 */
export function requestOpenCreatedSession(session: Session, goal: string): void {
  window.dispatchEvent(
    new CustomEvent<CreatedSessionDetail>(OPEN_CREATED_SESSION_EVENT, { detail: { session, goal } }),
  );
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
