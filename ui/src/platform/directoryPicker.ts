/**
 * Renderer-side accessor for the Electron native folder chooser — mirrors
 * ui/src/features/command-palette/badgeBridge.ts. The `Window.mads.dialog`
 * shape is declared once in ui/src/conversation/TerminalRenderer/ptyBridge.ts
 * (declaration merging requires every augmentation to agree); this file only
 * reads it.
 *
 * Undefined outside Electron: a plain browser can't hand back an absolute
 * path (directory inputs expose relative names only), so callers fall back to
 * a typed path.
 */
export interface DirectoryPicker {
  /** Resolves the chosen absolute path, or null if the user cancelled. */
  pickDirectory(options?: { title?: string; defaultPath?: string }): Promise<string | null>;
}

export function getDirectoryPicker(): DirectoryPicker | undefined {
  return typeof window !== "undefined" ? window.mads?.dialog : undefined;
}
