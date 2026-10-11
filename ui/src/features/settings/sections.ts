import type { Config } from "../../router/types";

/** Order matches the scroll-spy rail and the one-page section order. */
export type SectionId =
  | "profiles"
  | "sessions"
  | "models"
  | "workspaces"
  | "pipelines"
  | "synthesis"
  | "appearance"
  | "editor"
  | "credentials"
  | "experimental"
  | "system"
  | "about";

/**
 * The rail's groups, in page order; the nav draws a separator between them.
 * SECTION_IDS is their concatenation.
 */
export const SECTION_GROUPS: SectionId[][] = [
  ["profiles"],
  ["sessions", "models", "workspaces"],
  ["pipelines", "synthesis"],
  ["appearance", "editor", "credentials"],
  ["experimental", "system", "about"],
];

export const SECTION_IDS: SectionId[] = SECTION_GROUPS.flat();

export const SECTION_LABELS: Record<SectionId, string> = {
  profiles: "Profiles",
  sessions: "New sessions",
  models: "Agents & models",
  workspaces: "Workspaces",
  pipelines: "Pipelines",
  synthesis: "Synthesis",
  appearance: "Appearance",
  editor: "Editor",
  credentials: "Credentials & environment",
  experimental: "Experimental",
  system: "System",
  about: "About mads",
};

/** The muted line under each section's heading. Editor has none. */
export const SECTION_LEDES: Partial<Record<SectionId, string>> = {
  profiles: "A profile is a saved snapshot of everything on this page.",
  sessions: "What a session starts with when the Launchpad doesn't say otherwise.",
  models: "The harness your sessions run in, and the cheap model mads uses for background work.",
  workspaces: "Groups of repos a session can start in, with defaults of their own.",
  pipelines: "How multi-stage runs behave.",
  synthesis: "Summaries and retros mads writes after a session.",
  appearance: "Changes here apply instantly.",
  credentials: "Secrets mads stores, and what environment agents get.",
  experimental: "Features still in progress. Changes apply instantly.",
  system: "Health checks, background processes and housekeeping.",
  about: "The build you're running, and what mads is for.",
};

/** Sections whose controls save themselves (saveFields), never the save bar. */
export const LIVE_SECTIONS: ReadonlySet<SectionId> = new Set(["appearance", "experimental"]);

/** What an old or aliased section link asks for beyond scrolling to `section`. */
export type SectionIntent = "shells";

export interface ResolvedSection {
  section: SectionId;
  intent?: SectionIntent;
}

// Section ids from before the regroup, still in bookmarks, history and
// callers (the shell dock's "Manage" opens #shells).
const SECTION_ALIASES: Record<string, ResolvedSection> = {
  general: { section: "sessions" },
  pipeline: { section: "pipelines" },
  advanced: { section: "experimental" },
  providers: { section: "models" },
  updates: { section: "about" },
  shells: { section: "system", intent: "shells" },
};

/**
 * The section a link's hash (without "#") points at, following old ids to
 * where their content lives now. `intent` says what the link wanted there —
 * "shells" is the shell inventory, which lives inside System. Null for a hash
 * that names no section.
 */
export function resolveSectionHash(hash: string): ResolvedSection | null {
  if ((SECTION_IDS as string[]).includes(hash)) return { section: hash as SectionId };
  return SECTION_ALIASES[hash] ?? null;
}

/**
 * Which section a Config JSON field's input lives in — the single map the
 * page layout and the profile-switch diff dialog both read, so a field's
 * diff entry always points at the section where it can be edited.
 * Fields absent here (onboarding_complete, update_feed_url,
 * active_profile_id, credential_bindings, default_trust, default_template)
 * are either excluded from profile snapshots, handled by dedicated diff
 * logic (credential bindings), or not edited on this page and left out of
 * the diff as before (default_trust, default_template).
 * default_model, default_agent and default_isolation have no input here —
 * the Launchpad's "Save as my defaults" writes them — but a profile switch
 * that changes them lists them under New sessions.
 */
export const FIELD_SECTION: Partial<Record<keyof Config, SectionId>> = {
  default_workspace: "sessions",
  default_dir: "sessions",
  default_model: "sessions",
  default_agent: "sessions",
  default_isolation: "sessions",
  hide_delegates_in_session_list: "sessions",
  delegate_fade: "sessions",
  auto_commit_messages: "sessions",

  default_provider: "models",
  fast_model_mode: "models",
  fast_model: "models",
  fast_model_base_url: "models",
  fast_model_api_key_env: "models",

  concurrency_limit: "pipelines",
  pipeline_stage_timeout_minutes: "pipelines",
  cost_alert_cents: "pipelines",
  allow_sleep_during_pipelines: "pipelines",

  synthesis_mode: "synthesis",
  synthesis_auto_approve: "synthesis",
  default_model_tier: "synthesis",
  agent_selection_mode: "synthesis",
  synthesis_debounce_ms: "synthesis",

  theme: "appearance",
  palette: "appearance",
  display_mode: "appearance",
  reduce_motion: "appearance",
  stream_speed: "appearance",
  stream_wpm: "appearance",

  editor_command: "editor",
  keybindings: "editor",

  env_mode: "credentials",
  env_passthrough: "credentials",

  feature_flags: "experimental",

  log_retention_days: "system",
};

// The profile-switch diff shows stored values as-is, so negatively named
// fields are labelled by what they store, not by their switch's wording.
export const FIELD_LABELS: Partial<Record<keyof Config, string>> = {
  default_workspace: "Start in workspace",
  default_dir: "Start in folder",
  default_model: "Default model",
  default_agent: "Default persona",
  default_isolation: "Default isolation",
  hide_delegates_in_session_list: "Hide subagents from the session list",
  delegate_fade: "Hide finished subagents after",
  auto_commit_messages: "Write commit messages for me",

  default_provider: "Default provider",
  fast_model_mode: "Fast Model runs on",
  fast_model: "Fast Model model",
  fast_model_base_url: "Fast Model endpoint",
  fast_model_api_key_env: "Fast Model credential",

  concurrency_limit: "Concurrency limit",
  pipeline_stage_timeout_minutes: "Stage timeout (min)",
  cost_alert_cents: "Cost alert (cents)",
  allow_sleep_during_pipelines: "Let the computer sleep during runs",

  synthesis_mode: "Synthesis",
  synthesis_auto_approve: "Apply proposals automatically",
  default_model_tier: "Model tier",
  agent_selection_mode: "Agent selection",
  synthesis_debounce_ms: "Wait before synthesizing (ms)",

  theme: "Theme",
  palette: "Palette",
  display_mode: "Density",
  reduce_motion: "Reduce motion",
  stream_speed: "Reply streaming",
  stream_wpm: "Words per minute",

  editor_command: "Open files with",
  keybindings: "Keybindings",

  env_mode: "Environment",
  env_passthrough: "Also pass through",

  feature_flags: "Experimental features",

  log_retention_days: "Keep logs for (days)",
};
