import type { Config } from "../../router/types";

/** Order matches the scroll-spy rail and the one-page section order. */
export type SectionId =
  | "profiles"
  | "general"
  | "pipeline"
  | "appearance"
  | "credentials"
  | "advanced"
  | "providers"
  | "updates";

export const SECTION_IDS: SectionId[] = [
  "profiles",
  "general",
  "pipeline",
  "appearance",
  "credentials",
  "advanced",
  "providers",
  "updates",
];

export const SECTION_LABELS: Record<SectionId, string> = {
  profiles: "Profiles",
  general: "General",
  pipeline: "Pipeline & synthesis",
  appearance: "Appearance",
  credentials: "Credentials",
  advanced: "Advanced",
  providers: "Providers",
  updates: "Updates & About",
};

/**
 * Which section a Config JSON field's input lives in — the single map the
 * page layout and the profile-switch diff dialog both read, so a field's
 * diff entry always points at the section where it can be edited.
 * Fields absent here (onboarding_complete, update_feed_url,
 * active_profile_id, credential_bindings) are either excluded from profile
 * snapshots or handled by dedicated, non-generic diff logic.
 */
export const FIELD_SECTION: Partial<Record<keyof Config, SectionId>> = {
  concurrency_limit: "general",
  log_retention_days: "general",
  synthesis_debounce_ms: "general",
  editor_command: "general",
  display_mode: "general",
  keybindings: "general",
  default_workspace: "general",
  default_provider: "general",

  synthesis_mode: "pipeline",
  agent_selection_mode: "pipeline",
  default_model_tier: "pipeline",
  cost_alert_cents: "pipeline",
  fast_model: "pipeline",
  fast_model_base_url: "pipeline",
  fast_model_api_key_env: "pipeline",

  theme: "appearance",
  palette: "appearance",

  feature_flags: "advanced",

  env_mode: "credentials",
  env_passthrough: "credentials",
};

export const FIELD_LABELS: Partial<Record<keyof Config, string>> = {
  concurrency_limit: "Concurrency limit",
  log_retention_days: "Log retention (days)",
  synthesis_debounce_ms: "Synthesis debounce (ms)",
  editor_command: "Editor command",
  display_mode: "Display mode",
  keybindings: "Keybindings",
  default_workspace: "Default workspace",
  default_provider: "Default provider",

  synthesis_mode: "Synthesis mode",
  agent_selection_mode: "Agent selection mode",
  default_model_tier: "Default model tier",
  cost_alert_cents: "Cost alert (cents)",
  fast_model: "Fast model",
  fast_model_base_url: "Fast Model endpoint",
  fast_model_api_key_env: "Fast Model credential",

  theme: "Theme",
  palette: "Palette",

  feature_flags: "Feature flags",

  env_mode: "Environment mode",
  env_passthrough: "Environment passthrough",
};
