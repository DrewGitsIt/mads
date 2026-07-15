import type { PaletteName, ThemeMode } from "../../../router/types";

/**
 * Sets [data-theme]/[data-palette] on the document root — tokens.css already
 * defines all 6 combinations, this is the one place that runtime-applies
 * them. Resolves "system" via matchMedia; called immediately on click
 * (before any network round-trip resolves) so "live" actually means live.
 */
export function applyTheme(theme: ThemeMode, palette: PaletteName): void {
  const resolved: "dark" | "light" =
    theme === "system"
      ? window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light"
      : theme;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.dataset.palette = palette;
}
