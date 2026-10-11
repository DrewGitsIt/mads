import type { Config } from "../../router/types";

/**
 * Mirrors pkg/contracts/config.go's RestartRequiredFields exactly — that map
 * is the canonical set (feature_flags is NOT restart-required there), so
 * this stays presentation-only rather than round-tripping metadata through
 * Go for a single-client desktop app, but must not drift from it.
 */
export const RESTART_REQUIRED_FIELDS: ReadonlySet<keyof Config> = new Set([
  "concurrency_limit",
  "log_retention_days",
  "synthesis_debounce_ms",
]);
