import type { Config, ProfileSnapshot } from "../../router/types";

/**
 * Client-side mirror of pkg/contracts/profile.go's ProfileExcludedFields —
 * there's no Router-proxied capture/apply endpoint (Data Service's
 * POST /profiles/capture isn't exposed through Router), so Settings computes
 * snapshots itself against the same Config it already holds.
 */
export const PROFILE_EXCLUDED_FIELDS: ReadonlySet<string> = new Set([
  "onboarding_complete",
  "update_feed_url",
  "active_profile_id",
]);

/** Mirrors profile.go SnapshotConfig — captures every non-excluded field. */
export function snapshotFromConfig(config: Config): ProfileSnapshot {
  const snapshot: ProfileSnapshot = {};
  for (const [key, value] of Object.entries(config)) {
    if (PROFILE_EXCLUDED_FIELDS.has(key)) continue;
    snapshot[key] = value;
  }
  return snapshot;
}

/**
 * Mirrors profile.go ApplySnapshot — overlays every field snapshot captured
 * onto config; a field snapshot doesn't mention keeps config's own value.
 */
export function applySnapshot(config: Config, snapshot: ProfileSnapshot): Config {
  const merged = { ...config };
  for (const [key, value] of Object.entries(snapshot)) {
    if (PROFILE_EXCLUDED_FIELDS.has(key)) continue;
    (merged as Record<string, unknown>)[key] = value;
  }
  return merged;
}
