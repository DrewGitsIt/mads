import type { Monaco } from "@monaco-editor/react";
import type { Binding } from "../../platform/hotkeys";

/**
 * Maps a hotkeys.ts binding to a Monaco addCommand keybinding. KeyMod.CtrlCmd
 * is ⌘ on mac and Ctrl elsewhere, matching hotkeys.ts's `mod`. Undefined for a
 * key Monaco has no KeyCode mapping for here (only Enter, Escape and letters
 * are needed today).
 */
export function monacoKeybinding(
  b: Binding,
  monaco: Pick<Monaco, "KeyMod" | "KeyCode">,
): number | undefined {
  const k = b.key.toLowerCase();
  let code: number | undefined;
  if (k === "enter") code = monaco.KeyCode.Enter;
  else if (k === "escape") code = monaco.KeyCode.Escape;
  else if (/^[a-z]$/.test(k)) {
    code = (monaco.KeyCode as unknown as Record<string, number>)[
      `Key${k.toUpperCase()}`
    ];
  }
  if (code === undefined) return undefined;
  return (
    (b.mod ? monaco.KeyMod.CtrlCmd : 0) |
    (b.shift ? monaco.KeyMod.Shift : 0) |
    code
  );
}
