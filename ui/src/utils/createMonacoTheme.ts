import type { Monaco } from "@monaco-editor/react";

export const MADS_MONACO_THEME = "mads";

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/**
 * A palette token as the #rrggbbaa Monaco requires — its theme colors parse
 * hex only, and tokens.css mixes hex with rgba() (--accent-soft). `alpha`
 * scales the token's own alpha, for tints of a solid token. Undefined when
 * the token is unset or in a form this doesn't parse, so the inherited
 * base-theme color stays instead of a misparse.
 */
export function tokenHex(value: string, alpha = 1): string | undefined {
  let r: number, g: number, b: number;
  let a = 1;
  const hex = /^#([0-9a-f]{3,8})$/i.exec(value);
  const rgb = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)(?:[\s,/]+([\d.]+%?))?\s*\)$/i.exec(value);
  if (hex) {
    let h = hex[1];
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join("");
    if (h.length !== 6 && h.length !== 8) return undefined;
    [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
    if (h.length === 8) a = parseInt(h.slice(6, 8), 16) / 255;
  } else if (rgb) {
    [r, g, b] = [rgb[1], rgb[2], rgb[3]].map(Number);
    if (rgb[4]) a = rgb[4].endsWith("%") ? parseFloat(rgb[4]) / 100 : Number(rgb[4]);
  } else {
    return undefined;
  }
  const byte = (n: number) =>
    Math.round(Math.min(255, Math.max(0, n)))
      .toString(16)
      .padStart(2, "0");
  return `#${byte(r)}${byte(g)}${byte(b)}${byte(a * alpha * 255)}`;
}

/**
 * Registers a Monaco theme sourced from the app's live CSS custom properties
 * (tokens.css) rather than a hardcoded per-palette table — tokens.css already
 * resolves [data-theme]/[data-palette] into --bg1/--text/etc. on :root, so
 * reading getComputedStyle here tracks whichever palette is currently active
 * with no per-palette enumeration on this side. Call from `beforeMount` so
 * the theme exists before the Editor's `theme` prop applies it (see
 * ExpandedComposer, which also backs the terminal compose sheet), and again
 * when the palette changes (DiffViewer does).
 *
 * Diff colors are soft tints of --ok / --bad. Monaco paints a wholly
 * added or removed line with both the line and the text tint, so the two
 * together (~20%) are what a new line shows; vs-dark's ~40% olive made long
 * additions hard to read.
 */
export function defineMadsMonacoTheme(monaco: Monaco): void {
  const base = document.documentElement.dataset.theme === "light" ? "vs" : "vs-dark";
  const colors: Record<string, string | undefined> = {
    "editor.background": tokenHex(cssVar("--bg1")),
    "editor.foreground": tokenHex(cssVar("--text")),
    "editorGutter.background": tokenHex(cssVar("--bg1")),
    "editorLineNumber.foreground": tokenHex(cssVar("--text3")),
    "editorLineNumber.activeForeground": tokenHex(cssVar("--text2")),
    "editorCursor.foreground": tokenHex(cssVar("--accent")),
    "editor.selectionBackground": tokenHex(cssVar("--accent-soft")),
    "editor.lineHighlightBackground": tokenHex(cssVar("--bg2")),
    "editorWidget.background": tokenHex(cssVar("--bg2")),
    "editorWidget.border": tokenHex(cssVar("--line-soft")),
    "diffEditor.insertedLineBackground": tokenHex(cssVar("--ok"), 0.07),
    "diffEditor.insertedTextBackground": tokenHex(cssVar("--ok"), 0.14),
    "diffEditor.removedLineBackground": tokenHex(cssVar("--bad"), 0.08),
    "diffEditor.removedTextBackground": tokenHex(cssVar("--bad"), 0.16),
    "diffEditorGutter.insertedLineBackground": tokenHex(cssVar("--ok"), 0.14),
    "diffEditorGutter.removedLineBackground": tokenHex(cssVar("--bad"), 0.16),
    "diffEditor.border": tokenHex(cssVar("--line-soft")),
    "diffEditor.diagonalFill": tokenHex(cssVar("--line-soft")),
    "diffEditor.unchangedRegionBackground": tokenHex(cssVar("--bg2")),
    "diffEditor.unchangedRegionForeground": tokenHex(cssVar("--text3")),
    "diffEditor.unchangedCodeBackground": tokenHex(cssVar("--bg2"), 0.5),
  };
  monaco.editor.defineTheme(MADS_MONACO_THEME, {
    base,
    inherit: true,
    rules: [],
    colors: Object.fromEntries(Object.entries(colors).filter((e): e is [string, string] => e[1] !== undefined)),
  });
}
