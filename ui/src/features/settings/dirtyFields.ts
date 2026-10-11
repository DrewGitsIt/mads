import type { Config } from "../../router/types";
import { RESTART_REQUIRED_FIELDS } from "./restartRequired";

// Response-only facts about the Router's host (services/router's
// configResponse) ride along in GET /config but aren't settings the user can
// edit — same precedent as profileSnapshot's PROFILE_EXCLUDED_FIELDS.
const RESPONSE_ONLY_FIELDS: ReadonlySet<string> = new Set(["platform", "fallback_dir"]);

// An omitted optional field and its cleared value are the same setting: a
// text box typed into and emptied, or the last passthrough var removed, isn't
// an unsaved change.
function normalize(value: unknown): string {
  if (value === undefined || value === null || value === "") return "";
  if (Array.isArray(value) && value.length === 0) return "";
  return JSON.stringify(value);
}

/**
 * Top-level Config keys whose value differs between `saved` and `draft` — the
 * save bar's "N settings". Compound fields (credential_bindings,
 * env_passthrough, feature_flags) count once each.
 */
export function dirtyFields(saved: Config, draft: Config): (keyof Config)[] {
  const keys = new Set([...Object.keys(saved), ...Object.keys(draft)]);
  const dirty: (keyof Config)[] = [];
  for (const key of keys) {
    if (RESPONSE_ONLY_FIELDS.has(key)) continue;
    const field = key as keyof Config;
    if (normalize(saved[field]) !== normalize(draft[field])) dirty.push(field);
  }
  return dirty;
}

/** The subset of `fields` that only take effect when mads restarts. */
export function restartFieldsIn(fields: (keyof Config)[]): (keyof Config)[] {
  return fields.filter((f) => RESTART_REQUIRED_FIELDS.has(f));
}
