// Single formatter for user-visible shortcut hints (design doc F10). Handlers
// already accept metaKey || ctrlKey; only the rendered text differs per OS.
// The platform comes from the Router's Config.platform (runtime.GOOS), set
// once by App after the config gate. Until then — or in isolated component
// tests — navigator.platform is the fallback.
//
// Ctrl+` (shell dock) is deliberately NOT routed through here: it stays Ctrl
// on every platform, as in VS Code.

let routerPlatform: string | undefined;

/** Records the Router host's runtime.GOOS; called by App once config loads. */
export function setPlatform(platform: string | undefined): void {
  routerPlatform = platform;
}

function isMac(platform: string | undefined): boolean {
  if (platform !== undefined) return platform === "darwin";
  if (typeof navigator === "undefined") return false;
  return /^mac/i.test(navigator.platform ?? "");
}

const MAC_SYMBOLS: Record<string, string> = { Mod: "⌘", Shift: "⇧", Alt: "⌥" };
const OTHER_NAMES: Record<string, string> = { Mod: "Ctrl" };

/**
 * formatShortcut("Mod+K") → "⌘K" on darwin, "Ctrl+K" elsewhere.
 * `platform` overrides the stored/navigator platform (tests, previews).
 */
export function formatShortcut(combo: string, platform: string | undefined = routerPlatform): string {
  const parts = combo.split("+");
  if (isMac(platform)) return parts.map((p) => MAC_SYMBOLS[p] ?? p).join("");
  return parts.map((p) => OTHER_NAMES[p] ?? p).join("+");
}
