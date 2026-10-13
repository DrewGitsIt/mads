/**
 * Typed view of the tokens. Palette values come from palettes.ts, generated
 * alongside palettes.css from palettes.yaml; tokens.test.ts asserts the two
 * agree. Components read the CSS custom properties, never these constants —
 * this module exists for the gallery and the contrast checks.
 */

export interface ThemeTokens {
  bg0: string;
  bg1: string;
  bg2: string;
  bg3: string;
  bg4: string;
  line: string;
  lineSoft: string;
  text: string;
  text2: string;
  text3: string;
  accent: string;
  accentHover: string;
  accentSoft: string;
  accentInk: string;
  ok: string;
  okSoft: string;
  /** text on `okSoft` (chip labels); held to AA on it */
  okFg: string;
  warn: string;
  warnSoft: string;
  /** text on `warnSoft` (chip labels); held to AA on it */
  warnFg: string;
  bad: string;
  badSoft: string;
  /** text on `badSoft` (chip labels); held to AA on it */
  badFg: string;
  info: string;
  infoSoft: string;
  /** text on `infoSoft` (chip labels); held to AA on it */
  infoFg: string;
  chart1: string;
  chart2: string;
  chart3: string;
  shadow: string;
}

export type ThemeName = "dark" | "light";

// Generated from palettes.yaml by scripts/make-palettes.mjs.
export { paletteInfo, paletteNames, palettes } from "./palettes";
export type { PaletteInfo, PaletteName, PaletteTag } from "./palettes";

export const fonts = {
  mono: 'ui-monospace,"SF Mono","Cascadia Code","JetBrains Mono",Menlo,Consolas,monospace',
  sans: '-apple-system,BlinkMacSystemFont,"Segoe UI",Ubuntu,Roboto,"Helvetica Neue",sans-serif',
};

export const radius = {
  r: "8px",
  rSm: "6px",
};

/** 4/8px density scale (MADS-UX §2 rule 2). */
export const space = {
  1: "4px",
  2: "8px",
  3: "12px",
  4: "16px",
  5: "24px",
  6: "32px",
};

/** Prose reading measure (MADS-UX §3.3: full-width stream, ~90-92ch prose). */
export const measure = {
  prose: "92ch",
};
