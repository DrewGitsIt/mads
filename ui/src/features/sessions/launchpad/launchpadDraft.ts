import type { Agent, LinkRequest, Workspace } from "../../../router/types";
import type { StagedConfig } from "./pills/stagedConfig";

/**
 * The launchpad's unsent goal — text, staged pill settings and staged
 * links — held in memory for the lifetime of the app session. The
 * Launchpad unmounts whenever a session is open (SessionsSurface swaps it
 * for SessionWorkspace), so without this "+ New session" brought back an
 * empty form. Starting the session clears it. Its attachments are kept
 * the same way, by stagedAttachments.ts's launchpad list.
 */
export interface LaunchpadDraft {
  text: string;
  staged: StagedConfig;
  selectedWorkspace?: Workspace;
  selectedAgent?: Agent;
  stagedLinks: LinkRequest[];
}

const EMPTY: LaunchpadDraft = { text: "", staged: {}, stagedLinks: [] };

let draft: LaunchpadDraft = EMPTY;

export function getLaunchpadDraft(): LaunchpadDraft {
  return draft;
}

export function setLaunchpadDraft(next: LaunchpadDraft): void {
  draft = next;
}

export function clearLaunchpadDraft(): void {
  draft = EMPTY;
}
