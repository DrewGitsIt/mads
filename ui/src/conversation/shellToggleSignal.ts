// Cross-tree signal for the Ctrl+` shortcut: App.tsx owns the physical
// keydown listener but doesn't own `shellOpen` state (four layers down in
// ConversationTab.tsx), so it fires a plain window Event rather than
// threading a callback prop through every intermediate component.
export const SHELL_TOGGLE_EVENT = "mads:toggle-shell";

export function requestShellToggle() {
  window.dispatchEvent(new Event(SHELL_TOGGLE_EVENT));
}
