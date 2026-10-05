import type { SessionTrust } from "../../../../router/types";

/** Pill edits are staged client-side until session creation (MADS-UX §3.2) — never persisted individually. */
export interface StagedConfig {
  /** A folder picked on the folder pill; mutually exclusive with `workspace`. */
  dir?: string;
  workspace?: string;
  agent?: string;
  provider?: string;
  model?: string;
  trust?: SessionTrust;
  isolation?: string;
  multiRepoPaths?: string[];
}
