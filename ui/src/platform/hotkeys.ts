// Single binding table for the session dock's keyboard shortcuts. Matching is
// exact on modifiers: on Linux/Windows a plain Ctrl+letter is never bound
// (readline owns those in terminals), so every non-mac default carries Shift.
//
// A user override map (future Settings/Config) plugs in through
// setHotkeyOverrides() or the optional `overrides` argument of each function.
// An override replaces the binding on every platform.

import { isMac } from "./shortcut";

export type HotkeyAction =
  | "expandEditor" // expand / compose editor
  | "sendFromEditor"
  | "collapseEditor"
  | "toggleView" // chat <-> terminal
  | "attachFiles";

export interface Binding {
  /** KeyboardEvent.key, compared case-insensitively ("e", "Enter", "Escape"). */
  key: string;
  /** Requires the platform modifier: Cmd on mac, Ctrl elsewhere. False means no modifier at all. */
  mod: boolean;
  shift: boolean;
}

export type HotkeyOverrides = Partial<Record<HotkeyAction, Binding>>;

export const DEFAULT_BINDINGS: Record<HotkeyAction, { mac: Binding; other: Binding }> = {
  expandEditor: {
    mac: { key: "e", mod: true, shift: false },
    other: { key: "e", mod: true, shift: true },
  },
  sendFromEditor: {
    mac: { key: "Enter", mod: true, shift: false },
    other: { key: "Enter", mod: true, shift: false },
  },
  collapseEditor: {
    mac: { key: "Escape", mod: false, shift: false },
    other: { key: "Escape", mod: false, shift: false },
  },
  toggleView: {
    mac: { key: "t", mod: true, shift: true },
    other: { key: "t", mod: true, shift: true },
  },
  attachFiles: {
    mac: { key: "a", mod: true, shift: true },
    other: { key: "a", mod: true, shift: true },
  },
};

let userOverrides: HotkeyOverrides = {};

/** Replaces the user override map (pass {} to reset). */
export function setHotkeyOverrides(overrides: HotkeyOverrides): void {
  userOverrides = overrides;
}

/** The effective binding for `action`: override if present, else the platform default. */
export function bindingFor(
  action: HotkeyAction,
  overrides: HotkeyOverrides = userOverrides,
  platform?: string,
): Binding {
  const override = overrides[action];
  if (override) return override;
  const d = DEFAULT_BINDINGS[action];
  return macFor(platform) ? d.mac : d.other;
}

function macFor(platform: string | undefined): boolean {
  return isMac(platform);
}

type KeyLike = Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey" | "shiftKey">;

/** True when `e` is exactly the chord bound to `action` (no extra modifiers). */
export function matches(
  action: HotkeyAction,
  e: KeyLike,
  overrides: HotkeyOverrides = userOverrides,
  platform?: string,
): boolean {
  const b = bindingFor(action, overrides, platform);
  if (e.key.toLowerCase() !== b.key.toLowerCase()) return false;
  if (e.altKey || e.shiftKey !== b.shift) return false;
  if (macFor(platform)) {
    return b.mod ? e.metaKey && !e.ctrlKey : !e.metaKey && !e.ctrlKey;
  }
  return b.mod ? e.ctrlKey && !e.metaKey : !e.ctrlKey && !e.metaKey;
}

const KEY_LABELS: Record<string, string> = { enter: "⏎", escape: "Esc" };

/** UI hint text for an action: "⌘E", "Ctrl+Shift+E", "⌘⏎", "Esc". */
export function hotkeyLabel(
  action: HotkeyAction,
  overrides: HotkeyOverrides = userOverrides,
  platform?: string,
): string {
  const b = bindingFor(action, overrides, platform);
  const k = KEY_LABELS[b.key.toLowerCase()] ?? (b.key.length === 1 ? b.key.toUpperCase() : b.key);
  if (macFor(platform)) return `${b.mod ? "⌘" : ""}${b.shift ? "⇧" : ""}${k}`;
  return [b.mod ? "Ctrl" : "", b.shift ? "Shift" : "", k].filter(Boolean).join("+");
}
