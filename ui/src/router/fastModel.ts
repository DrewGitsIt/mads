import type { Config, FastModelMode, FastModelStatus } from "./types";

// effectiveFastModelMode mirrors pkg/contracts/config.go's
// Config.FastModelEffectiveMode: an unset fast_model_mode (pre-mode-selector
// install) resolves to endpoint if an endpoint was already configured, else
// the new zero-config provider default.
export function effectiveFastModelMode(config: Config): FastModelMode {
  if (config.fast_model_mode) return config.fast_model_mode;
  return config.fast_model_base_url ? "endpoint" : "provider";
}

/**
 * Whether a Fast Model can actually answer right now: off never, an endpoint
 * once it has a URL, the provider only when GET /fast-model/status says it
 * completes in the background. A status still loading reads as no — the
 * features that need one ("Draft with Fast Model", one-click Commit) appear
 * once it's known rather than offering something that then fails.
 */
export function fastModelAvailable(config: Config, status: FastModelStatus | null | undefined): boolean {
  const mode = effectiveFastModelMode(config);
  if (mode === "off") return false;
  if (mode === "endpoint") return Boolean(config.fast_model_base_url);
  return Boolean(status?.provider_completer);
}
