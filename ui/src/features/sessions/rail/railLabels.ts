import { displayPath } from "../../../files/displayPath";
import { EMPTY_AGENT_INDEX, agentLabel } from "../../../router/agentRef";
import type { AgentIndex } from "../../../router/agentRef";
import { contextOccupancy, contextWindowForModel } from "../../../router/modelDisplay";
import { sessionLocation } from "../../../router/sessionLocation";
import type { Session, Workspace } from "../../../router/types";
import { workspaceIndex } from "../../../router/workspaceRef";

/**
 * What the rail knows about a workspace, keyed by every reference a session may
 * carry — Session.workspace is an id-or-name reference (router/workspaceRef.ts,
 * the one place that resolves one).
 */
export interface WorkspaceFacts {
  /** The workspace's own name — what a card prints, never the `wsp_...` reference. */
  name: string;
}

export function workspaceFacts(workspaces: Workspace[]): Map<string, WorkspaceFacts> {
  const facts = new Map<string, WorkspaceFacts>();
  for (const [ref, ws] of workspaceIndex(workspaces)) facts.set(ref, { name: ws.name });
  return facts;
}

export interface RailLabelContext {
  facts: Map<string, WorkspaceFacts>;
  /** Resolves Session.agent, an id-or-name reference (router/agentRef.ts). */
  agents: AgentIndex;
}

export const EMPTY_LABEL_CONTEXT: RailLabelContext = {
  facts: new Map(),
  agents: EMPTY_AGENT_INDEX,
};

/** The labels for one card. Every field absent is a normal, common state. */
export interface RailLabels {
  /**
   * Which project this is: the workspace name, else the chosen folder's
   * basename (router/sessionLocation.ts — the same rule the conversation
   * header's subline uses).
   */
  location?: string;
  /** Isolation's branch, in mono — also the existing "has unmerged work" flag. */
  branch?: string;
  /** Retro sessions read differently; they get a prefix glyph rather than a word. */
  retro: boolean;
}

/**
 * The card's context line: `location · branch`.
 *
 * Location is always shown — "which project is this card?" is what the rail
 * is scanned for, and it used to be answered only while the visible list
 * happened to span two workspaces, or when a cwd differed from the
 * workspace's primary repo. A label that came and went with a filter also
 * cost the rail a height-reservation scheme to stop cards reflowing; a label
 * that is a fixed fact about the session needs none. The full folder (and an
 * isolated session's worktree) is in the tooltip.
 *
 * The branch follows whenever the session is isolated — the signal that work
 * is sitting unmerged. Its "→ base" target is left to the header: a 280px
 * card has room for one branch name, not two.
 *
 * The persona used to take a slot here and no longer does: the harness mark
 * on the title row is what actually differs between neighbouring cards. It
 * is still in railTooltip, with everything else the resting card drops.
 */
export function railLabelsFor(session: Session, ctx: RailLabelContext): RailLabels {
  return {
    location: sessionLocation(session, workspaceLabel(session, ctx.facts)).name,
    // A branch only exists for an isolated session, and one whose worktree
    // hasn't been provisioned yet has no branch to show.
    branch: session.worktree_branch || undefined,
    retro: Boolean(session.retro_id) || (session.tags ?? []).includes("retro"),
  };
}

/** Whether this session has anything at all to put on the context line. */
export function hasRailLabels(labels: RailLabels): boolean {
  return Boolean(labels.location || labels.branch || labels.retro);
}

/** The workspace name for a session's reference, never the raw reference alone. */
function workspaceLabel(session: Session, facts: Map<string, WorkspaceFacts>): string | undefined {
  if (!session.workspace) return undefined;
  return facts.get(session.workspace)?.name || session.workspace;
}

function line(key: string, value: string | undefined): string[] {
  return value ? [`${key}: ${value}`] : [];
}

/**
 * The hover tooltip: every labelling field on the record, including the ones
 * the resting card drops — the full folder behind the location's basename,
 * and an isolated session's worktree, which is where its agent really runs.
 */
export function railTooltip(session: Session, ctx: RailLabelContext = EMPTY_LABEL_CONTEXT): string {
  const occupancy = contextOccupancy(session.last_usage, contextWindowForModel(session.model));
  const where = sessionLocation(session);
  return [
    ...line("Workspace", workspaceLabel(session, ctx.facts)),
    // Through sessionLocation, exactly as the card is: an isolation:current_dir
    // session stores cwd "." — the absence of a chosen directory — and
    // "Folder: ." is not an answer to "where is this running".
    ...line("Folder", where.folder && displayPath(where.folder)),
    ...line("Worktree", where.worktree && displayPath(where.worktree)),
    ...line("Branch", session.worktree_branch),
    ...line("Agent", agentLabel(session.agent, ctx.agents)),
    ...line("Model", session.model),
    ...line("Provider", session.provider),
    // A level the provider can't run is shown with what it runs as instead
    // (effective_trust: auto_review → limited on a harness with no reviewer).
    ...line(
      "Trust",
      session.trust && session.effective_trust
        ? `${session.trust} → ${session.effective_trust}`
        : session.trust,
    ),
    ...line("Isolation", session.isolation),
    ...line(
      "Context",
      occupancy ? `${occupancy.pct}% of ${Math.round(occupancy.window / 1000)}k` : undefined,
    ),
  ].join("\n");
}
