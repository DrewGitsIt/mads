#!/usr/bin/env node
// make-palettes.mjs — turn ui/src/tokens/palettes.yaml into the palette tokens.
//
//   node scripts/make-palettes.mjs                         convert (offline)
//   node scripts/make-palettes.mjs export <name> <state>   snapshot one palette
//
// The palettes were designed in ChromaConscious
// (https://drewkidwell.com/chromaconscious). Each one's export is committed as
// ui/src/tokens/chromaconscious/<name>.json: every color token and the six
// 12-step ramps, light and dark, plus the link that reopens the theme. `export`
// refreshes one snapshot from a state link (the payload after #s=); it is the
// only step that touches the network.
//
// Converting reads the snapshots and palettes.yaml, maps ChromaConscious's
// shadcn-style tokens onto mads's 30, applies the yaml's overrides, checks every
// pair tokens.test.ts asserts, and writes (all committed):
//   ui/src/tokens/palettes.css  one [data-palette][data-theme] block per combo
//   ui/src/tokens/palettes.ts   the same values typed, plus names and labels
// Nothing is written if any pair misses WCAG AA.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const tokensDir = join(root, "ui", "src", "tokens");
const snapDir = join(tokensDir, "chromaconscious");
const require = createRequire(join(root, "ui", "package.json"));
const YAML = require("yaml");

const API = "https://drewkidwell.com/api/chromaconscious/v1";
const AA = 4.5;
const MODES = ["dark", "light"];
const ROLES = ["primary", "accent", "neutral", "danger", "success", "warning"];
// --find-current-fg in tokens.css: fixed dark text on the current find match.
const FIND_CURRENT_FG = "#1a1408";

// ---------- color math (WCAG 2 relative luminance, sRGB mixing) ----------

const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
const toHex = (c) => "#" + c.map((v) => Math.round(Math.min(255, Math.max(0, v))).toString(16).padStart(2, "0")).join("");
const mix = (a, b, t) => toHex(rgb(a).map((x, i) => x + (rgb(b)[i] - x) * t));
const rgba = (hex, a) => `rgba(${rgb(hex).join(",")},${a})`;

function luminance(c) {
  const ch = (v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2]);
}

function contrast(a, b) {
  const [la, lb] = [luminance(a), luminance(b)];
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** A hex or rgba() color, composited over an opaque hex background. */
function over(color, bg) {
  if (color.startsWith("#")) return rgb(color);
  const [r, g, b, a] = color.match(/[\d.]+/g).map(Number);
  return [r, g, b].map((v, i) => v * a + rgb(bg)[i] * (1 - a));
}

const ratio = (fg, bg, base) => contrast(over(fg, base ?? bg), over(bg, base ?? bg));

// ---------- export ----------

async function get(path) {
  const res = await fetch(`${API}/${path}`);
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res;
}

async function exportPalette(name, state) {
  const css = await (await get(`export?state=${state}&format=css`)).text();
  const [, theme, url] = css.match(/^\/\* ChromaConscious (t_\w+) · (\S+) \*\//) ?? [];
  if (!theme) throw new Error(`no ChromaConscious header in the CSS export for ${name}`);
  const snap = { theme, url };
  for (const mode of MODES) {
    const fig = await (await get(`export?state=${state}&format=figma&mode=${mode}`)).json();
    const hex = (v) => v.$value.hex.toLowerCase().slice(0, 7);
    snap[mode] = {
      color: Object.fromEntries(Object.entries(fig.color).filter(([k]) => k !== "scrim").map(([k, v]) => [k, hex(v)])),
      ramp: Object.fromEntries(ROLES.map((r) => [r, Array.from({ length: 12 }, (_, i) => hex(fig.ramp[r][i + 1]))])),
    };
  }
  mkdirSync(snapDir, { recursive: true });
  writeFileSync(join(snapDir, `${name}.json`), JSON.stringify(snap, null, 2) + "\n");
  console.log(`exported ${name} (${theme})`);
}

// ---------- convert ----------

const SURFACES = ["bg0", "bg1", "bg2", "bg3", "bg4", "line", "lineSoft"];

/** Step 11 eased toward step 8 as far as AA on bg0 and bg1 allows: a third, quieter text step. */
function fitText3(t, n) {
  let best = n[10];
  for (let i = 1; i <= 40; i++) {
    const c = mix(n[10], n[7], i / 40);
    if (Math.min(ratio(c, t.bg0), ratio(c, t.bg1)) < AA + 0.1) break;
    best = c;
  }
  return best;
}

function toMads(snap, mode, palette) {
  const { color: c, ramp } = snap[mode];
  const n = ramp.neutral; // 0-based: n[0] is step 1
  const p = ramp.primary;
  const overrides = palette.overrides?.[mode] ?? {};
  const t = {
    // Steps 1–6 are surfaces and lines; 11–12 are text (ChromaConscious ramps).
    bg0: n[0], bg1: n[1], bg2: n[2], bg3: n[3], bg4: n[4],
    line: n[5], lineSoft: mix(n[3], n[4], 0.5),
    text: n[11], text2: n[10], text3: "",
    accent: c.primary, accentHover: "", accentSoft: "", accentInk: c["primary-foreground"],
  };
  // ChromaConscious has no hover tokens. Step 10 is the hover of the raw seat,
  // but the engine solves --primary off the seat, so derive the hover from the
  // fill itself: 12% away from its text, so hovering only adds contrast.
  const lighter = luminance(rgb(t.text)) > luminance(rgb(t.bg0)) ? t.text : t.bg0;
  const darker = lighter === t.text ? t.bg0 : t.text;
  const inkIsDark = luminance(rgb(t.accentInk)) < luminance(rgb(t.accent));
  t.accentHover = mix(t.accent, inkIsDark ? lighter : darker, 0.12);
  // Dark mode: ChromaConscious darkens the fill until light text passes. mads
  // wants the bright seat (step 9) with near-black text (step 1), hover at
  // step 10, wherever both hold AA. High-contrast palettes keep the engine's.
  if (mode === "dark" && !palette.high_contrast && Math.min(ratio(p[0], p[8]), ratio(p[0], p[9])) >= AA) {
    Object.assign(t, { accent: p[8], accentHover: p[9], accentInk: p[0] });
  }
  Object.assign(t, pick(overrides, SURFACES));
  const ownSurfaces = SURFACES.some((k) => k in overrides);

  t.text3 = fitText3(t, n);
  t.accentSoft = rgba(t.accent, mode === "dark" ? 0.16 : 0.14);
  for (const [k, role] of [["ok", "success"], ["warn", "warning"], ["bad", "destructive"]]) {
    t[k] = c[`${role}-strong`];
    t[`${k}Fg`] = c[`${role}-subtle-foreground`];
    // -subtle-foreground is solved against -subtle on the engine's own page.
    // On hand-set surfaces, tint those instead, as mads always has.
    t[`${k}Soft`] = ownSurfaces ? rgba(t[k], 0.14) : c[`${role}-subtle`];
  }
  t.info = c["accent-strong"];
  t.infoSoft = ownSurfaces ? rgba(t.info, 0.13) : c.accent;
  t.infoFg = ratio(c.link, t.infoSoft, t.bg1) >= AA ? c.link : c["accent-foreground"];
  Object.assign(t, { chart1: c["chart-1"], chart2: c["chart-2"], chart3: c["chart-3"] });
  t.shadow = mode === "dark" ? "0 12px 28px rgba(0, 0, 0, 0.5)" : `0 12px 28px ${rgba(n[11], 0.16)}`;
  return Object.assign(t, overrides);
}

const pick = (o, keys) => Object.fromEntries(keys.filter((k) => k in o).map((k) => [k, o[k]]));

/** Every pair tokens.test.ts asserts, plus the hard-coded find highlight and the pressed neutral chip. */
function contrastPairs(t) {
  return [
    ["text on bg0", t.text, t.bg0],
    ["text on bg1", t.text, t.bg1],
    ["text on bg4", t.text, t.bg4],
    ["text2 on bg0", t.text2, t.bg0],
    ["text2 on bg1", t.text2, t.bg1],
    ["text2 on bg2", t.text2, t.bg2],
    ["text3 on bg0", t.text3, t.bg0],
    ["text3 on bg1", t.text3, t.bg1],
    ["accentInk on accent", t.accentInk, t.accent],
    ["accentInk on accentHover", t.accentInk, t.accentHover],
    ["okFg on okSoft", t.okFg, t.okSoft, t.bg1],
    ["warnFg on warnSoft", t.warnFg, t.warnSoft, t.bg1],
    ["badFg on badSoft", t.badFg, t.badSoft, t.bg1],
    ["infoFg on infoSoft", t.infoFg, t.infoSoft, t.bg1],
    ["find-current-fg on warn", FIND_CURRENT_FG, t.warn],
  ];
}

const VAR = (k) => k.replace(/[A-Z]/g, (m) => "-" + m.toLowerCase()).replace(/(chart)(\d)/, "$1-$2");
const ORDER = [
  "bg0", "bg1", "bg2", "bg3", "bg4", "line", "lineSoft", "", "text", "text2", "text3", "",
  "accent", "accentHover", "accentSoft", "accentInk", "",
  "ok", "okSoft", "okFg", "warn", "warnSoft", "warnFg", "bad", "badSoft", "badFg", "info", "infoSoft", "infoFg", "",
  "chart1", "chart2", "chart3", "", "shadow",
];

function cssBlock(selectors, t) {
  const body = ORDER.map((k) => (k ? `  --${VAR(k)}: ${t[k]};` : "")).join("\n");
  return `${selectors.join(",\n")} {\n${body}\n}\n`;
}

const HEADER = "Generated by scripts/make-palettes.mjs from palettes.yaml and chromaconscious/*.json. Do not edit.";

function convert() {
  const list = YAML.parse(readFileSync(join(tokensDir, "palettes.yaml"), "utf8"));
  const out = [];
  const failures = [];
  for (const palette of list) {
    const snap = JSON.parse(readFileSync(join(snapDir, `${palette.name}.json`), "utf8"));
    const modes = Object.fromEntries(MODES.map((m) => [m, toMads(snap, m, palette)]));
    for (const m of MODES) {
      for (const [label, fg, bg, base] of contrastPairs(modes[m])) {
        const r = ratio(fg, bg, base);
        if (r < AA) failures.push(`${palette.name} ${m}: ${label} ${r.toFixed(2)}:1`);
      }
    }
    out.push({ palette, snap, modes });
  }
  if (failures.length) {
    console.error("WCAG AA failures; nothing written:\n  " + failures.join("\n  "));
    process.exit(1);
  }

  const [first] = out;
  const css = [`/* ${HEADER}\n * Each palette's ChromaConscious link is in palettes.ts (\`source\`). */\n`];
  for (const { palette, modes } of out) {
    for (const m of MODES) {
      const sel = [`[data-palette="${palette.name}"][data-theme="${m}"]`];
      // The default palette also answers when [data-palette] is absent; dark is :root's default.
      if (palette === first.palette) sel.unshift(...(m === "dark" ? [":root", `[data-theme="dark"]:not([data-palette])`] : [`[data-theme="light"]:not([data-palette])`]));
      css.push(cssBlock(sel, modes[m]));
    }
  }
  writeFileSync(join(tokensDir, "palettes.css"), css.join("\n"));

  const q = JSON.stringify;
  const tokens = (t) => "{\n" + ORDER.filter(Boolean).map((k) => `      ${k}: ${q(t[k])},`).join("\n") + "\n    }";
  const ts = `// ${HEADER}
import type { ThemeName, ThemeTokens } from "./tokens";

export const paletteNames = [${out.map((o) => q(o.palette.name)).join(", ")}] as const;
export type PaletteName = (typeof paletteNames)[number];

export type PaletteTag = "warm" | "cool" | "green" | "neutral" | "muted" | "balanced" | "vivid" | "high-contrast";

export interface PaletteInfo {
  name: PaletteName;
  label: string;
  description: string;
  tags: readonly PaletteTag[];
  /** Reopens the palette's ChromaConscious theme. */
  source: string;
}

/** Picker order; the first is the default. */
export const paletteInfo: readonly PaletteInfo[] = [
${out.map(({ palette: p, snap }) => `  { name: ${q(p.name)}, label: ${q(p.label)}, description: ${q(p.description)}, tags: ${q(p.tags)}, source: ${q(snap.url)} },`).join("\n")}
];

export const palettes: Record<PaletteName, Record<ThemeName, ThemeTokens>> = {
${out.map(({ palette, modes }) => `  ${palette.name}: {\n${MODES.map((m) => `    ${m}: ${tokens(modes[m])},`).join("\n")}\n  },`).join("\n")}
};
`;
  writeFileSync(join(tokensDir, "palettes.ts"), ts);
  console.log(`wrote palettes.css and palettes.ts: ${out.length} palettes × ${MODES.length} themes`);
}

const [cmd, ...args] = process.argv.slice(2);
if (cmd === "export") {
  if (args.length !== 2) throw new Error("usage: make-palettes.mjs export <name> <state>");
  await exportPalette(args[0], args[1].replace(/^.*#s=/, ""));
} else if (cmd === undefined) {
  convert();
} else {
  throw new Error(`unknown command ${cmd}; run with no arguments to convert, or "export <name> <state>"`);
}
