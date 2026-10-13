import type { StagedConfig } from "../../../features/sessions/pills/stagedConfig";
import {
  NEW_SESSION_PLACEHOLDER,
  type LinkCadence,
  type LinkRequest,
  type Session,
} from "../../../router/types";

/** /new and its three linked variants. */
export type NewVariant = "new" | "leader" | "follower" | "monitor";

export const DEFAULT_CADENCE: LinkCadence = "on_signal";

/**
 * The staged link a variant creates with the session, in the same POST
 * /sessions (one endpoint is "$new"):
 *   /new-leader   leader preset — the new session leads this one
 *   /new-follower leader preset — this session leads the new one
 *   /new-monitor  monitor preset — the new session watches this one
 */
export function stagedLinkFor(
  variant: NewVariant,
  sessionId: string,
  cadence: LinkCadence,
): LinkRequest | undefined {
  switch (variant) {
    case "leader":
      return {
        granting_session_id: NEW_SESSION_PLACEHOLDER,
        subject_session_id: sessionId,
        preset: "leader",
        cadence,
      };
    case "follower":
      return {
        granting_session_id: sessionId,
        subject_session_id: NEW_SESSION_PLACEHOLDER,
        preset: "leader",
        cadence,
      };
    case "monitor":
      return {
        granting_session_id: NEW_SESSION_PLACEHOLDER,
        subject_session_id: sessionId,
        preset: "monitor",
        cadence,
      };
    default:
      return undefined;
  }
}

/**
 * The pills a /new card starts with: this session's own folder, persona,
 * provider + model, trust and isolation — all staged, so the new session
 * gets exactly these unless the user edits one. The folder is the one the
 * session asked for (its worktree, if any, is made from it), falling back
 * to its workspace.
 */
export function stagedFromSession(session: Session | undefined): StagedConfig {
  if (!session) return {};
  const staged: StagedConfig = {};
  if (session.dir) staged.dir = session.dir;
  else if (session.workspace) staged.workspace = session.workspace;
  if (session.agent) staged.agent = session.agent;
  if (session.provider) {
    staged.provider = session.provider;
    staged.model = session.model;
  }
  if (session.trust) staged.trust = session.trust;
  if (session.isolation) staged.isolation = session.isolation;
  return staged;
}
