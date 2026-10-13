import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { parse as parseYaml } from "yaml";
import {
  paletteNames,
  palettes,
  type PaletteName,
  type ThemeName,
  type ThemeTokens,
} from "./tokens";

/**
 * The palettes are generated (scripts/make-palettes.mjs, from palettes.yaml).
 * This asserts (a) palettes.css and palettes.ts agree across every
 * palette×theme, (b) every combination meets WCAG AA on each documented fg/bg
 * pair — operationalizes MADS-UX §2 rule 3 ("All text meets WCAG AA") — and
 * (c) the yaml, the generated names and pkg/contracts Palettes haven't drifted.
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

/** Alpha-composite a hex or rgba() color over an opaque hex background. */
function compositeOverHex(fg: string, bgHex: string): [number, number, number] {
  if (fg.startsWith("#")) return hexToRgb(fg);
  const [br, bg, bb] = hexToRgb(bgHex);
  const [fr, fgc, fb, a] = parseRgba(fg);
  return [fr * a + br * (1 - a), fgc * a + bg * (1 - a), fb * a + bb * (1 - a)];
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

const read = (path: string) => readFileSync(resolve(process.cwd(), path), "utf-8");
const palettesCss = read("src/tokens/palettes.css");
const tokensCss = read("src/tokens/tokens.css");

const THEMES: ThemeName[] = ["dark", "light"];
const COMBOS: [PaletteName, ThemeName, string][] = paletteNames.flatMap((palette) =>
  THEMES.map((theme): [PaletteName, ThemeName, string] => [
    palette,
    theme,
    `[data-palette="${palette}"][data-theme="${theme}"]`,
  ]),
);

describe("palettes.css matches palettes.ts", () => {
  it.each(COMBOS)("%s %s", (palette, theme, selector) => {
    const cssVars = parseCssBlock(palettesCss, selector);
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
    const cssVars = parseCssBlock(palettesCss, selector);
    const tsTheme = palettes[palette][theme];
    expect(cssVars.shadow.replace(/\s+/g, "")).toBe(
      tsTheme.shadow.replace(/\s+/g, ""),
    );
  });

  it("defaults to the first palette, dark, when no [data-palette] attribute is set", () => {
    expect(parseCssBlock(palettesCss, ":root")).toEqual(
      parseCssBlock(palettesCss, `[data-palette="${paletteNames[0]}"][data-theme="dark"]`),
    );
  });

  it("is imported by tokens.css", () => {
    expect(tokensCss).toMatch(/^@import "\.\/palettes\.css";/);
  });
});

describe("palette registry", () => {
  it("palettes.ts is generated from the current palettes.yaml (re-run scripts/make-palettes.mjs)", () => {
    const yaml = parseYaml(read("src/tokens/palettes.yaml")) as { name: string }[];
    expect(yaml.map((p) => p.name)).toEqual([...paletteNames]);
  });

  it("pkg/contracts Palettes lists the same palettes in the same order", () => {
    const go = read("../pkg/contracts/config.go");
    const list = /var Palettes = \[\]string\{([^}]*)\}/.exec(go)?.[1] ?? "";
    expect([...list.matchAll(/"(\w+)"/g)].map((m) => m[1])).toEqual([...paletteNames]);
  });
});

const BODY_TEXT_MIN = 4.5;
// --find-current-fg in tokens.css: fixed dark text on the current find match (--warn).
const FIND_CURRENT_FG = /--find-current-fg:\s*(#[0-9a-f]{6})/i.exec(tokensCss)?.[1] ?? "";

/** fg/bg pairs every theme must satisfy, in AA body-text terms (4.5:1). */
function bodyTextPairs(t: ThemeTokens) {
  return [
    { name: "text on bg0", fg: t.text, bg: t.bg0 },
    { name: "text on bg1", fg: t.text, bg: t.bg1 },
    { name: "text on bg4 (pressed neutral chip)", fg: t.text, bg: t.bg4 },
    { name: "text2 on bg0", fg: t.text2, bg: t.bg0 },
    { name: "text2 on bg1", fg: t.text2, bg: t.bg1 },
    { name: "text2 on bg2", fg: t.text2, bg: t.bg2 },
    { name: "text3 on bg0", fg: t.text3, bg: t.bg0 },
    { name: "text3 on bg1", fg: t.text3, bg: t.bg1 },
    { name: "accentInk on accent", fg: t.accentInk, bg: t.accent },
    { name: "accentInk on accentHover", fg: t.accentInk, bg: t.accentHover },
    { name: "find-current-fg on warn", fg: FIND_CURRENT_FG, bg: t.warn },
  ] as const;
}

/** semantic tone text sitting on its own tinted (soft) background — small chip labels. */
function toneOnSoftPairs(t: ThemeTokens) {
  return [
    { name: "okFg on okSoft/bg1", fg: t.okFg, softBg: t.okSoft, bg: t.bg1 },
    { name: "warnFg on warnSoft/bg1", fg: t.warnFg, softBg: t.warnSoft, bg: t.bg1 },
    { name: "badFg on badSoft/bg1", fg: t.badFg, softBg: t.badSoft, bg: t.bg1 },
    { name: "infoFg on infoSoft/bg1", fg: t.infoFg, softBg: t.infoSoft, bg: t.bg1 },
  ] as const;
}

describe.each(COMBOS)("%s %s WCAG AA contrast", (palette, theme) => {
  const t = palettes[palette][theme];

  it.each(bodyTextPairs(t))("$name meets $$4.5:1 (body text)", ({ fg, bg }) => {
    const ratio = contrastRatio(hexToRgb(fg), hexToRgb(bg));
    expect(ratio).toBeGreaterThanOrEqual(BODY_TEXT_MIN);
  });

  it.each(toneOnSoftPairs(t))(
    "$name meets $$4.5:1 (small chip text on tinted background)",
    ({ fg, softBg, bg }) => {
      const ratio = contrastRatio(hexToRgb(fg), compositeOverHex(softBg, bg));
      expect(ratio).toBeGreaterThanOrEqual(BODY_TEXT_MIN);
    },
  );
});
