import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { darkTheme, lightTheme, themes, type ThemeName } from "./tokens";

/**
 * (a) pins every dark-theme hex to the exact value from ux-prototype.html
 * (regression guard), (b) pins the light-theme values chosen here, (c) runs a
 * WCAG contrast-ratio check over every documented fg/bg pair in both themes,
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

const CSS_TO_THEME_KEY: Record<string, keyof typeof darkTheme> = {
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

describe("tokens.css matches tokens.ts", () => {
  const cases: [ThemeName, string][] = [
    ["dark", '[data-theme="dark"]'],
    ["light", '[data-theme="light"]'],
  ];

  it.each(cases)("%s theme", (themeName, selector) => {
    const cssVars = parseCssBlock(tokensCss, selector);
    const tsTheme = themes[themeName];
    for (const [cssName, tsKey] of Object.entries(CSS_TO_THEME_KEY)) {
      expect(
        normalizeColor(cssVars[cssName]),
        `--${cssName} (css) vs ${tsKey} (ts)`,
      ).toBe(normalizeColor(tsTheme[tsKey]));
    }
  });
});

const BODY_TEXT_MIN = 4.5;

/** fg/bg pairs every theme must satisfy, in AA body-text terms (4.5:1). */
function bodyTextPairs(t: typeof darkTheme) {
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
function toneOnSoftPairs(t: typeof darkTheme) {
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
  it("pins every dark-theme hex to the approved palette (docs/ux-prototype.html)", () => {
    expect(darkTheme).toEqual({
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
      badFg: "#DD7A66",
      info: "#96AFC7",
      infoSoft: "rgba(150,175,199,.13)",
      infoFg: "#96AFC7",
      chart1: "#C1763A",
      chart2: "#5E8FC9",
      chart3: "#A98836",
    });
  });

  it("pins the light-theme values chosen here (first pass, pending gallery sign-off)", () => {
    expect(lightTheme).toEqual({
      bg0: "#F7F3EC",
      bg1: "#F1EBE0",
      bg2: "#E9E1D2",
      bg3: "#DFD5C2",
      bg4: "#D2C6AE",
      line: "#C9BBA0",
      lineSoft: "#DCD0BC",
      text: "#241F18",
      text2: "#574E3E",
      text3: "#6B6048",
      accent: "#C08552",
      accentSoft: "rgba(192,133,82,.15)",
      accentInk: "#241305",
      ok: "#7CB389",
      okSoft: "rgba(124,179,137,.14)",
      okFg: "#2E5A3C",
      warn: "#D3AF5E",
      warnSoft: "rgba(211,175,94,.13)",
      warnFg: "#7A5A1A",
      bad: "#D2604A",
      badSoft: "rgba(210,96,74,.13)",
      badFg: "#8A3A2A",
      info: "#96AFC7",
      infoSoft: "rgba(150,175,199,.13)",
      infoFg: "#375069",
      chart1: "#C1763A",
      chart2: "#5E8FC9",
      chart3: "#A98836",
    });
  });

  const themeNames: ThemeName[] = ["dark", "light"];

  describe.each(themeNames)("%s theme WCAG AA contrast", (name) => {
    const t = themes[name];

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
