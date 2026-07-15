import type { Config } from "../../router/types";

/**
 * UI-owned classification (HANDOFF §7) — REBUILD-SPEC §10 only requires that
 * the UI indicate which changes need a restart, it doesn't prescribe a wire
 * contract, so this stays presentation-only rather than round-tripping
 * metadata through Go for a single-client desktop app.
 */
export const RESTART_REQUIRED_FIELDS: ReadonlySet<keyof Config> = new Set([
  "concurrency_limit",
  "log_retention_days",
  "feature_flags",
]);

/** Fields in RESTART_REQUIRED_FIELDS whose value differs between a and b. */
export function dirtyRestartFields(a: Config, b: Config): (keyof Config)[] {
  const dirty: (keyof Config)[] = [];
  for (const field of RESTART_REQUIRED_FIELDS) {
    if (JSON.stringify(a[field]) !== JSON.stringify(b[field])) dirty.push(field);
  }
  return dirty;
}
