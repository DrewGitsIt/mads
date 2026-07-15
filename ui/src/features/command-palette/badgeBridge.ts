/**
 * Renderer-side accessor for the Electron dock badge — mirrors
 * ui/src/conversation/TerminalRenderer/ptyBridge.ts's `window.mads.*`
 * pattern. The `Window.mads.badge` shape itself is declared once in
 * ptyBridge.ts (TypeScript's declaration merging requires every `declare
 * global` augmenting `Window.mads` to agree on shape) — this file only
 * reads it.
 */
export interface BadgeBridge {
  setCount(count: number): void;
}

export function getBadgeBridge(): BadgeBridge | undefined {
  return typeof window !== "undefined" ? window.mads?.badge : undefined;
}
