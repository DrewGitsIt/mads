import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  clayDark,
  clayLight,
  spaceDark,
  spaceLight,
  glacierDark,
  glacierLight,
  palettes,
  type PaletteName,
  type ThemeName,
  type ThemeTokens,
} from "./tokens";

/**
 * (a) pins every palette×theme's hex/rgba values to the approved source
 * (docs/palette-mockups.html, regression guard), (b) asserts tokens.css and
 * tokens.ts stay byte-identical across all 6 combinations, (c) runs a WCAG
 * contrast-ratio check over every documented fg/bg pair in all 6 combos,
 * asserting AA (4.5:1 body text, 3:1 large text/UI) — operationalizes
 * MADS-UX §2 rule 3 ("All text meets WCAG AA").
 */

function hexToRgb(hex: string): [number, number, number] {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) throw new Error(`not a 6-digit hex color: ${hex}`);
  const n = parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

function parseRgba(value: string): [number, number, number, number] {
  const m = /^rgba\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*\)$/.exec(
    value,
  );
  if (!m) throw new Error(`not an rgba() color: ${value}`);
  return [Number(m[1]), Number(m[2]), Number(m[3]), Number(m[4])];
}

/** Alpha-composite an rgba() color over an opaque hex background. */
function compositeOverHex(
  fgRgba: [number, number, number, number],
  bgHex: string,
): [number, number, number] {
  const [br, bg, bb] = hexToRgb(bgHex);
  const [fr, fg, fb, a] = fgRgba;
  return [fr * a + br * (1 - a), fg * a + bg * (1 - a), fb * a + bb * (1 - a)];
}

function channelLuminance(c: number): number {
  const cs = c / 255;
  return cs <= 0.03928 ? cs / 12.92 : Math.pow((cs + 0.055) / 1.055, 2.4);
}

function relativeLuminance([r, g, b]: [number, number, number]): number {
  return (
    0.2126 * channelLuminance(r) +
    0.7152 * channelLuminance(g) +
    0.0722 * channelLuminance(b)
  );
}

function contrastRatio(
  a: [number, number, number],
  b: [number, number, number],
): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const lighter = Math.max(la, lb);
  const darker = Math.min(la, lb);
  return (lighter + 0.05) / (darker + 0.05);
}

/** Extracts a `{ selector }` block's `--name: value;` declarations, resolving same-block `var(--x)` refs. */
function parseCssBlock(css: string, selector: string): Record<string, string> {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const blockStart = stripped.indexOf(selector);
  if (blockStart === -1) throw new Error(`selector not found: ${selector}`);
  const braceStart = stripped.indexOf("{", blockStart);
  const braceEnd = stripped.indexOf("}", braceStart);
  const body = stripped.slice(braceStart + 1, braceEnd);
  const vars: Record<string, string> = {};
  const declRe = /--([\w-]+):\s*([^;]+);/g;
  let m: RegExpExecArray | null;
  while ((m = declRe.exec(body))) {
    let value = m[2].trim().replace(/\s+/g, " ");
    const varRefMatch = /^var\(--([\w-]+)\)$/.exec(value);
    if (varRefMatch) value = vars[varRefMatch[1]];
    vars[m[1]] = value;
  }
  return vars;
}

/** Normalizes a hex or rgba() color string for cross-format comparison (case, spacing, leading zeros). */
function normalizeColor(value: string): string {
  if (value.startsWith("#")) return value.toLowerCase();
  const m = /^rgba\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*\)$/.exec(
    value,
  );
  if (!m) return value;
  const [, r, g, b, a] = m;
  return `rgba(${Number(r)},${Number(g)},${Number(b)},${Number(a)})`;
}

const CSS_TO_THEME_KEY: Record<string, keyof ThemeTokens> = {
  bg0: "bg0",
  bg1: "bg1",
  bg2: "bg2",
  bg3: "bg3",
  bg4: "bg4",
  line: "line",
  "line-soft": "lineSoft",
  text: "text",
  text2: "text2",
  text3: "text3",
  accent: "accent",
  "accent-hover": "accentHover",
  "accent-soft": "accentSoft",
  "accent-ink": "accentInk",
  ok: "ok",
  "ok-soft": "okSoft",
  "ok-fg": "okFg",
  warn: "warn",
  "warn-soft": "warnSoft",
  "warn-fg": "warnFg",
  bad: "bad",
  "bad-soft": "badSoft",
  "bad-fg": "badFg",
  info: "info",
  "info-soft": "infoSoft",
  "info-fg": "infoFg",
  "chart-1": "chart1",
  "chart-2": "chart2",
  "chart-3": "chart3",
};

const tokensCss = readFileSync(
  resolve(process.cwd(), "src/tokens/tokens.css"),
  "utf-8",
);

const COMBOS: [PaletteName, ThemeName, string][] = [
  ["clay", "dark", '[data-palette="clay"][data-theme="dark"]'],
  ["clay", "light", '[data-palette="clay"][data-theme="light"]'],
  ["space", "dark", '[data-palette="space"][data-theme="dark"]'],
  ["space", "light", '[data-palette="space"][data-theme="light"]'],
  ["glacier", "dark", '[data-palette="glacier"][data-theme="dark"]'],
  ["glacier", "light", '[data-palette="glacier"][data-theme="light"]'],
];

describe("tokens.css matches tokens.ts", () => {
  it.each(COMBOS)("%s %s", (palette, theme, selector) => {
    const cssVars = parseCssBlock(tokensCss, selector);
    const tsTheme = palettes[palette][theme];
    for (const [cssName, tsKey] of Object.entries(CSS_TO_THEME_KEY)) {
      expect(
        normalizeColor(cssVars[cssName]),
        `--${cssName} (css) vs ${tsKey} (ts) [${palette}/${theme}]`,
      ).toBe(normalizeColor(tsTheme[tsKey]));
    }
  });

  // --shadow is `<offset> <blur> rgba(...)`, not a bare color — normalizeColor()
  // falls through unchanged for it, so compare after collapsing all whitespace
  // rather than routing it through CSS_TO_THEME_KEY (avoids spurious failures
  // from spacing style differences between the CSS and TS literals).
  it.each(COMBOS)("%s %s --shadow", (palette, theme, selector) => {
    const cssVars = parseCssBlock(tokensCss, selector);
    const tsTheme = palettes[palette][theme];
    expect(cssVars.shadow.replace(/\s+/g, "")).toBe(
      tsTheme.shadow.replace(/\s+/g, ""),
    );
  });

  it("defaults to clay dark when no [data-palette] attribute is set", () => {
    expect(parseCssBlock(tokensCss, ":root")).toEqual(
      parseCssBlock(tokensCss, '[data-palette="clay"][data-theme="dark"]'),
    );
  });
});

const BODY_TEXT_MIN = 4.5;

/** fg/bg pairs every theme must satisfy, in AA body-text terms (4.5:1). */
function bodyTextPairs(t: ThemeTokens) {
  return [
    { name: "text on bg0", fg: t.text, bg: t.bg0 },
    { name: "text on bg1", fg: t.text, bg: t.bg1 },
    { name: "text2 on bg0", fg: t.text2, bg: t.bg0 },
    { name: "text2 on bg1", fg: t.text2, bg: t.bg1 },
    { name: "text2 on bg2", fg: t.text2, bg: t.bg2 },
    { name: "text3 on bg0", fg: t.text3, bg: t.bg0 },
    { name: "text3 on bg1", fg: t.text3, bg: t.bg1 },
    { name: "accentInk on accent", fg: t.accentInk, bg: t.accent },
  ] as const;
}

/** semantic tone text sitting on its own tinted (soft) background — small chip labels. */
function toneOnSoftPairs(t: ThemeTokens) {
  return [
    { name: "okFg on okSoft/bg1", fg: t.okFg, softBg: t.okSoft, bg: t.bg1 },
    {
      name: "warnFg on warnSoft/bg1",
      fg: t.warnFg,
      softBg: t.warnSoft,
      bg: t.bg1,
    },
    { name: "badFg on badSoft/bg1", fg: t.badFg, softBg: t.badSoft, bg: t.bg1 },
    {
      name: "infoFg on infoSoft/bg1",
      fg: t.infoFg,
      softBg: t.infoSoft,
      bg: t.bg1,
    },
  ] as const;
}

describe("tokens", () => {
  it("pins clay dark to the approved palette (docs/palette-mockups.html)", () => {
    expect(clayDark).toEqual({
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
    });
  });

  it("pins clay light to the approved palette (docs/palette-mockups.html)", () => {
    expect(clayLight).toEqual({
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
    });
  });

  it("pins space dark to the approved palette (docs/palette-mockups.html)", () => {
    expect(spaceDark).toEqual({
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
    });
  });

  it("pins space light to the approved palette (docs/palette-mockups.html)", () => {
    expect(spaceLight).toEqual({
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
    });
  });

  it("pins glacier dark to the approved palette (docs/palette-mockups.html)", () => {
    expect(glacierDark).toEqual({
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
    });
  });

  it("pins glacier light to the approved palette (docs/palette-mockups.html)", () => {
    expect(glacierLight).toEqual({
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
    });
  });

  describe.each(COMBOS)("%s %s WCAG AA contrast", (palette, theme) => {
    const t = palettes[palette][theme];

    it.each(bodyTextPairs(t))(
      "$name meets $$4.5:1 (body text)",
      ({ fg, bg }) => {
        const ratio = contrastRatio(hexToRgb(fg), hexToRgb(bg));
        expect(ratio).toBeGreaterThanOrEqual(BODY_TEXT_MIN);
      },
    );

    it.each(toneOnSoftPairs(t))(
      "$name meets $$4.5:1 (small chip text on tinted background)",
      ({ fg, softBg, bg }) => {
        const composited = compositeOverHex(parseRgba(softBg), bg);
        const ratio = contrastRatio(hexToRgb(fg), composited);
        expect(ratio).toBeGreaterThanOrEqual(BODY_TEXT_MIN);
      },
    );
  });
});
