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
