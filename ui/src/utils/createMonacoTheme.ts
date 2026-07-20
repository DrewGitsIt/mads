import type { Monaco } from "@monaco-editor/react";

export const MADS_MONACO_THEME = "mads";

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/**
 * Registers a Monaco theme sourced from the app's live CSS custom properties
 * (tokens.css) rather than a hardcoded per-palette table — tokens.css already
 * resolves [data-theme]/[data-palette] into --bg1/--text/etc. on :root, so
 * reading getComputedStyle here tracks whichever of the 6 combinations is
 * currently active with no per-palette enumeration on this side. Call from
 * `beforeMount` so the theme exists before the Editor's `theme` prop applies
 * it (see ExpandedComposer/TerminalComposer).
 */
export function defineMadsMonacoTheme(monaco: Monaco): void {
  const base = document.documentElement.dataset.theme === "light" ? "vs" : "vs-dark";
  monaco.editor.defineTheme(MADS_MONACO_THEME, {
    base,
    inherit: true,
    rules: [],
    colors: {
      "editor.background": cssVar("--bg1"),
      "editor.foreground": cssVar("--text"),
      "editorLineNumber.foreground": cssVar("--text3"),
      "editorLineNumber.activeForeground": cssVar("--text2"),
      "editorCursor.foreground": cssVar("--accent"),
      "editor.selectionBackground": cssVar("--accent-soft"),
      "editor.lineHighlightBackground": cssVar("--bg2"),
      "editorWidget.background": cssVar("--bg2"),
      "editorWidget.border": cssVar("--line-soft"),
    },
  });
}
