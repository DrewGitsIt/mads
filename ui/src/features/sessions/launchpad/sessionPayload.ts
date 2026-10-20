import type { CreateSessionPayload, LinkRequest } from "../../../router/types";
import type { StagedConfig } from "../pills/stagedConfig";

/**
 * Backend requires a non-empty Session.Name with no server-side derivation —
 * derive one from the first line of the goal text, stored in full. The
 * header and rail truncate long names with CSS, not by cutting the stored
 * string; the router job (services/router/internal/job/title.go) replaces
 * this placeholder with a generated title after the session's first turn.
 */
export function deriveSessionName(text: string): string {
  return text.trim().split("\n")[0];
}

export interface CreateSessionExtras {
  /** The agent an unstaged pill resolves to — a saved Auto default needs the prompt as much as a picked one. */
  resolvedAgent?: string;
  /** Staged links, the new session's end written as NEW_SESSION_PLACEHOLDER. */
  links?: LinkRequest[];
  /** The draft whose staged attachments move to the new session. */
  attachmentDraft?: string;
  /**
   * The chosen provider runs at full trust only (fullAccessOnly): the
   * session goes out at Full whatever trust was staged, since the provider
   * would refuse anything else at Start.
   */
  fullTrustOnly?: boolean;
}

/**
 * The POST /sessions (and /sessions/validate) body for a goal plus staged
 * pills — shared by the Launchpad and the composer's /new card so both
 * derive the name and pass the opening prompt the same way.
 */
export function buildCreateSessionPayload(
  goal: string,
  staged: StagedConfig,
  extras: CreateSessionExtras = {},
): CreateSessionPayload {
  const trimmed = goal.trim();
  return {
    name: deriveSessionName(trimmed),
    name_source: "derived",
    dir: staged.dir,
    workspace: staged.workspace,
    agent: staged.agent,
    provider: staged.provider,
    model: staged.model,
    trust: extras.fullTrustOnly ? "full" : staged.trust,
    isolation: staged.isolation,
    ...((staged.agent ?? extras.resolvedAgent) === "auto" ? { opening_prompt: trimmed } : {}),
    ...(extras.links && extras.links.length > 0 ? { links: extras.links } : {}),
    ...(extras.attachmentDraft ? { attachment_draft: extras.attachmentDraft } : {}),
  };
}
