/**
 * Typed re-export of the token values declared in tokens.css.
 * tokens.test.ts asserts these stay byte-identical to tokens.css so there is
 * exactly one source of truth for the palette (components must read the CSS
 * custom properties, never these constants directly — this module exists for
 * the regression pin and the contrast checker only).
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
  accentSoft: string;
  accentInk: string;
  ok: string;
  okSoft: string;
  /** text-on-ok-soft color; equals `ok` unless the contrast checker demands otherwise */
  okFg: string;
  warn: string;
  warnSoft: string;
  /** text-on-warn-soft color; equals `warn` unless the contrast checker demands otherwise */
  warnFg: string;
  bad: string;
  badSoft: string;
  /** text-on-bad-soft color; equals `bad` unless the contrast checker demands otherwise */
  badFg: string;
  info: string;
  infoSoft: string;
  /** text-on-info-soft color; equals `info` unless the contrast checker demands otherwise */
  infoFg: string;
  chart1: string;
  chart2: string;
  chart3: string;
}

/** Verbatim from docs/ux-prototype.html `:root` block and the COST_SER array. */
export const darkTheme: ThemeTokens = {
  bg0: "#1A1713",
  bg1: "#201D18",
  bg2: "#27231D",
  bg3: "#2F2A23",
  bg4: "#3C352C",
  line: "#3A342A",
  lineSoft: "#2C2720",
  text: "#EDE9DF",
  text2: "#B2AA97",
  text3: "#8E8674",
  accent: "#C08552",
  accentSoft: "rgba(192,133,82,.15)",
  accentInk: "#241305",
  ok: "#7CB389",
  okSoft: "rgba(124,179,137,.14)",
  okFg: "#7CB389",
  warn: "#D3AF5E",
  warnSoft: "rgba(211,175,94,.13)",
  warnFg: "#D3AF5E",
  bad: "#D2604A",
  badSoft: "rgba(210,96,74,.13)",
  // lightened brick, same ~10deg hue as --bad: the base hue's text-on-soft
  // contrast against dark grounds falls just short of AA (tokens.test.ts)
  badFg: "#DD7A66",
  info: "#96AFC7",
  infoSoft: "rgba(150,175,199,.13)",
  infoFg: "#96AFC7",
  chart1: "#C1763A",
  chart2: "#5E8FC9",
  chart3: "#A98836",
};

/**
 * NOT in any source doc — no light-theme hex values exist in docs/. Derived by
 * inverting the dark scale's lightness within the same warm-sepia hue family,
 * carrying accent/semantic/chart hues over unchanged (theme-invariant).
 * First pass pending human sign-off via the gallery review.
 */
export const lightTheme: ThemeTokens = {
  bg0: "#F7F3EC",
  bg1: "#F1EBE0",
  bg2: "#E9E1D2",
  bg3: "#DFD5C2",
  bg4: "#D2C6AE",
  line: "#C9BBA0",
  lineSoft: "#DCD0BC",
  text: "#241F18",
  text2: "#574E3E",
  // darkened from the initial #83775F derivation, which failed AA against bg0/
  // bg1 (tokens.test.ts caught this) — same warm-sepia hue family, lower lightness
  text3: "#6B6048",
  accent: darkTheme.accent,
  accentSoft: darkTheme.accentSoft,
  accentInk: darkTheme.accentInk,
  ok: darkTheme.ok,
  okSoft: darkTheme.okSoft,
  // darkened sage, same ~134deg hue as --ok: text-on-soft fails AA against the
  // light neutral scale at the base hue's luminance (tokens.test.ts caught this)
  okFg: "#2E5A3C",
  warn: darkTheme.warn,
  warnSoft: darkTheme.warnSoft,
  // darkened gold, same ~41deg hue as --warn: see okFg comment
  warnFg: "#7A5A1A",
  bad: darkTheme.bad,
  badSoft: darkTheme.badSoft,
  // darkened brick, same ~10deg hue as --bad: see okFg comment
  badFg: "#8A3A2A",
  info: darkTheme.info,
  infoSoft: darkTheme.infoSoft,
  // darkened slate blue, same ~210deg hue as --info: see okFg comment
  infoFg: "#375069",
  chart1: darkTheme.chart1,
  chart2: darkTheme.chart2,
  chart3: darkTheme.chart3,
};

export type ThemeName = "dark" | "light";

export const themes: Record<ThemeName, ThemeTokens> = {
  dark: darkTheme,
  light: lightTheme,
};

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
