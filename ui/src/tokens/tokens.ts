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
  accentHover: string;
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
  shadow: string;
}

export type PaletteName = "clay" | "space" | "glacier";
export type ThemeName = "dark" | "light";

/** Approved via docs/palette-mockups.html gallery review — clay is the shipping default. */
export const clayDark: ThemeTokens = {
  bg0: "#120F0A",
  bg1: "#1C1914",
  bg2: "#242019",
  bg3: "#2F2A23",
  bg4: "#3C352C",
  line: "#463E30",
  lineSoft: "#322C23",
  text: "#EDE9DF",
  text2: "#B2AA97",
  text3: "#8E8674",
  accent: "#C08552",
  accentHover: "#D3945E",
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
  badFg: "#DD7A66",
  info: "#96AFC7",
  infoSoft: "rgba(150,175,199,.13)",
  infoFg: "#96AFC7",
  chart1: "#C1763A",
  chart2: "#5E8FC9",
  chart3: "#A98836",
  shadow: "0 12px 28px rgba(0, 0, 0, 0.45)",
};

export const clayLight: ThemeTokens = {
  bg0: "#FAF7F1",
  bg1: "#F1EBE0",
  bg2: "#E9E1D2",
  bg3: "#DFD5C2",
  bg4: "#D2C6AE",
  line: "#BFAE8E",
  lineSoft: "#D8CCB6",
  text: "#241F18",
  text2: "#574E3E",
  text3: "#6B6048",
  accent: "#B1743F",
  accentHover: "#C08552",
  accentSoft: "rgba(192,133,82,.18)",
  accentInk: "#241305",
  ok: "#7CB389",
  okSoft: "rgba(124,179,137,.16)",
  okFg: "#2E5A3C",
  warn: "#D3AF5E",
  warnSoft: "rgba(211,175,94,.15)",
  warnFg: "#7A5A1A",
  bad: "#D2604A",
  badSoft: "rgba(210,96,74,.15)",
  badFg: "#8A3A2A",
  info: "#96AFC7",
  infoSoft: "rgba(150,175,199,.15)",
  infoFg: "#375069",
  chart1: "#C1763A",
  chart2: "#5E8FC9",
  chart3: "#A98836",
  shadow: "0 12px 28px rgba(80, 64, 40, 0.18)",
};

export const spaceDark: ThemeTokens = {
  bg0: "#05070F",
  bg1: "#0C0C14",
  bg2: "#14141F",
  bg3: "#1C1B2A",
  bg4: "#252330",
  line: "#34314A",
  lineSoft: "#232135",
  text: "#E9E9F4",
  text2: "#A8A8C0",
  text3: "#7D7C94",
  accent: "#8D7CF4",
  accentHover: "#A08FFC",
  accentSoft: "rgba(141,124,244,.16)",
  accentInk: "#100B26",
  ok: "#77BD92",
  okSoft: "rgba(119,189,146,.14)",
  okFg: "#77BD92",
  warn: "#D4B165",
  warnSoft: "rgba(212,177,101,.13)",
  warnFg: "#D4B165",
  bad: "#DD6F58",
  badSoft: "rgba(221,111,88,.13)",
  badFg: "#E58270",
  info: "#92A8D8",
  infoSoft: "rgba(146,168,216,.13)",
  infoFg: "#92A8D8",
  chart1: "#7C66E8",
  chart2: "#2F9E8F",
  chart3: "#A98836",
  shadow: "0 12px 28px rgba(0, 0, 0, 0.55)",
};

export const spaceLight: ThemeTokens = {
  bg0: "#F5F4FA",
  bg1: "#ECECF4",
  bg2: "#E2E1EE",
  bg3: "#D5D4E6",
  bg4: "#C5C3DB",
  line: "#A9A6C6",
  lineSoft: "#CCCAE0",
  text: "#1D1B2A",
  text2: "#4C4963",
  text3: "#615E7C",
  accent: "#6A55D8",
  accentHover: "#7C66E8",
  accentSoft: "rgba(106,85,216,.14)",
  accentInk: "#F4F1FF",
  ok: "#77BD92",
  okSoft: "rgba(119,189,146,.16)",
  okFg: "#2C5C40",
  warn: "#D4B165",
  warnSoft: "rgba(212,177,101,.15)",
  warnFg: "#75561A",
  bad: "#DD6F58",
  badSoft: "rgba(221,111,88,.15)",
  badFg: "#8E3A29",
  info: "#92A8D8",
  infoSoft: "rgba(146,168,216,.15)",
  infoFg: "#3C4A78",
  chart1: "#7C66E8",
  chart2: "#2F9E8F",
  chart3: "#A98836",
  shadow: "0 12px 28px rgba(50, 45, 90, 0.16)",
};

export const glacierDark: ThemeTokens = {
  bg0: "#03101D",
  bg1: "#051C31",
  bg2: "#09263F",
  bg3: "#0E304E",
  bg4: "#143A5C",
  line: "#20507A",
  lineSoft: "#0D2C47",
  text: "#E6EEF6",
  text2: "#A4BDD1",
  text3: "#7793AB",
  accent: "#65A5C8",
  accentHover: "#7CB8D8",
  accentSoft: "rgba(101,165,200,.16)",
  accentInk: "#041220",
  ok: "#6FC2A0",
  okSoft: "rgba(111,194,160,.14)",
  okFg: "#6FC2A0",
  warn: "#D4B165",
  warnSoft: "rgba(212,177,101,.13)",
  warnFg: "#D4B165",
  bad: "#E0705A",
  badSoft: "rgba(224,112,90,.13)",
  badFg: "#E88472",
  info: "#AFBFD2",
  infoSoft: "rgba(175,191,210,.13)",
  infoFg: "#AFBFD2",
  chart1: "#4A90C2",
  chart2: "#A98836",
  chart3: "#9A6FC9",
  shadow: "0 12px 28px rgba(0, 0, 0, 0.5)",
};

export const glacierLight: ThemeTokens = {
  bg0: "#F5F8FB",
  bg1: "#ECF1F7",
  bg2: "#E0E8F1",
  bg3: "#D0DCE9",
  bg4: "#BCCDDF",
  line: "#96AFC5",
  lineSoft: "#C5D3E0",
  text: "#12293C",
  text2: "#3B5468",
  text3: "#54718A",
  accent: "#076C9A",
  accentHover: "#0A80B5",
  accentSoft: "rgba(7,108,154,.13)",
  accentInk: "#EAF4FA",
  ok: "#6FC2A0",
  okSoft: "rgba(111,194,160,.16)",
  okFg: "#1F5C44",
  warn: "#D4B165",
  warnSoft: "rgba(212,177,101,.15)",
  warnFg: "#75561A",
  bad: "#E0705A",
  badSoft: "rgba(224,112,90,.15)",
  badFg: "#8E3A29",
  info: "#5B7BA0",
  infoSoft: "rgba(91,123,160,.14)",
  infoFg: "#33506E",
  chart1: "#4A90C2",
  chart2: "#A98836",
  chart3: "#9A6FC9",
  shadow: "0 12px 28px rgba(30, 60, 90, 0.15)",
};

export const palettes: Record<PaletteName, Record<ThemeName, ThemeTokens>> = {
  clay: { dark: clayDark, light: clayLight },
  space: { dark: spaceDark, light: spaceLight },
  glacier: { dark: glacierDark, light: glacierLight },
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

/** Prose reading measure (MADS-UX §3.3: full-width stream, ~90-92ch prose). */
export const measure = {
  prose: "92ch",
};
