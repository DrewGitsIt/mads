// Cross-tree signal for the Ctrl+` shortcut: App.tsx owns the physical
// keydown listener but doesn't own `shellOpen` state (four layers down in
// ConversationTab.tsx), so it fires a plain window Event rather than
// threading a callback prop through every intermediate component.
export const SHELL_TOGGLE_EVENT = "mads:toggle-shell";

export function requestShellToggle() {
  window.dispatchEvent(new Event(SHELL_TOGGLE_EVENT));
}

/**
 * True for the Ctrl+` chord. Matches the physical key (`code`) as well as the
 * character: Electron on Linux/Wayland reports `key: "Unidentified"` for
 * Ctrl+` (seen 2026-11-19 on COSMIC), so a `key`-only check never fires there.
 * `key` stays for layouts whose backtick sits on a different physical key.
 */
export function isShellToggleKey(e: Pick<KeyboardEvent, "ctrlKey" | "key" | "code">): boolean {
  return e.ctrlKey && (e.code === "Backquote" || e.key === "`");
}
