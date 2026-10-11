#!/usr/bin/env node
// make-icon.mjs — regenerate the app icon from ui/assets/icon/mads.svg.
//
//   node scripts/make-icon.mjs
//
// Writes, next to the SVG (all committed — nothing renders it at build time):
//   mads.icns  macOS bundle icon; ensure-electron.sh copies it into mads.app
//   mads.png   512px, the Linux window icon and the macOS Dock fallback
//
// macOS only: the .icns comes from iconutil and the downscaling from sips.
// There is no SVG rasterizer on a stock Mac, so the SVG is rendered once at
// 1024px by the Chromium the Playwright suite already installs (ui/e2e). The
// "m" is live text in SF Mono, the app's own --mono on macOS; Chromium knows
// neither ui-monospace nor "SF Mono" by name, so the page loads the system
// font file under that name.
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const iconDir = join(root, "ui", "assets", "icon");
const require = createRequire(join(root, "ui", "package.json"));
const { chromium } = require("@playwright/test");
const SF_MONO = "/System/Library/Fonts/SFNSMono.ttf";

const work = mkdtempSync(join(tmpdir(), "mads-icon-"));
try {
  const master = join(work, "master.png");
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1024, height: 1024 } });
    const svg = readFileSync(join(iconDir, "mads.svg"), "utf8");
    // A file:// page, not setContent: about:blank may not load a file:// font.
    const html = join(work, "icon.html");
    writeFileSync(
      html,
      `<style>
        @font-face { font-family: "SF Mono"; src: url("${pathToFileURL(SF_MONO)}"); }
        html, body { margin: 0; background: transparent }
        svg { display: block }
      </style>${svg}`,
    );
    await page.goto(pathToFileURL(html).href);
    // fonts.check() is true for any unknown family; load() lists real faces.
    if (!(await page.evaluate(async () => (await document.fonts.load('700 1em "SF Mono"')).length))) {
      throw new Error(`make-icon: could not load ${SF_MONO}`);
    }
    await page.screenshot({ path: master, omitBackground: true });
  } finally {
    await browser.close();
  }

  const sips = (size, out) =>
    execFileSync("sips", ["-z", String(size), String(size), master, "--out", out], {
      stdio: "ignore",
    });

  const iconset = join(work, "mads.iconset");
  mkdirSync(iconset);
  for (const size of [16, 32, 128, 256, 512]) {
    sips(size, join(iconset, `icon_${size}x${size}.png`));
    sips(size * 2, join(iconset, `icon_${size}x${size}@2x.png`));
  }
  execFileSync("iconutil", ["-c", "icns", iconset, "-o", join(iconDir, "mads.icns")]);
  copyFileSync(join(iconset, "icon_512x512.png"), join(iconDir, "mads.png"));
  console.log(`make-icon: wrote ${join(iconDir, "mads.icns")} and mads.png`);
} finally {
  rmSync(work, { recursive: true, force: true });
}
