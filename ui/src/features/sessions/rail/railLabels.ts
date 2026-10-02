import { displayPath } from "../../../files/displayPath";
import { EMPTY_AGENT_INDEX, agentNameForRef } from "../../../router/agentRef";
import type { AgentIndex } from "../../../router/agentRef";
import { contextOccupancy, contextWindowForModel } from "../../../router/modelDisplay";
import type { Session, Workspace } from "../../../router/types";
import { workspaceIndex } from "../../../router/workspaceRef";

/**
 * What the rail knows about a workspace, keyed by every reference a session may
 * carry — Session.workspace is an id-or-name reference (router/workspaceRef.ts,
 * the one place that resolves one), and a workspace holds a list of repos with
 * at most one marked primary.
 */
export interface WorkspaceFacts {
  /** The workspace's own name — what a card prints, never the `wsp_...` reference. */
  name: string;
  /** The repo that anchors worktree isolation (contracts/registry.go) — the path a cwd is judged redundant against. */
  primaryRepo?: string;
  /** The workspace's default persona; a session bound to it is running the default, which is not worth a label. */
  defaultAgent?: string;
}

export function workspaceFacts(workspaces: Workspace[]): Map<string, WorkspaceFacts> {
  const facts = new Map<string, WorkspaceFacts>();
  for (const [ref, ws] of workspaceIndex(workspaces)) {
    // A single-repo workspace may leave `primary` unset — with one repo
    // there is nothing to disambiguate, and it is still the anchor.
    const repos = ws.repos ?? [];
    const primary = repos.find((r) => r.primary) ?? repos[0];
    facts.set(ref, {
      name: ws.name,
      primaryRepo: primary?.path,
      defaultAgent: ws.defaults.agent ?? ws.defaults.persona,
    });
  }
  return facts;
}

export interface RailLabelContext {
  /**
   * Whether the *visible* list spans more than one workspace. A workspace name
   * that every card would repeat says nothing, so it only earns the card when
   * the list is mixed — which means this flips as filters narrow the list, and
   * is exactly why the context line's height must not depend on its contents.
   */
  multiWorkspace: boolean;
  /**
   * The same question asked of the whole rail rather than the visible slice —
   * which is what reservesContextLine() needs, and the only reason a card's
   * line can ever change content without the rail's membership changing.
   */
  railMultiWorkspace: boolean;
  facts: Map<string, WorkspaceFacts>;
  /** Resolves Session.agent, an id-or-name reference (router/agentRef.ts). */
  agents: AgentIndex;
}

export const EMPTY_LABEL_CONTEXT: RailLabelContext = {
  multiWorkspace: false,
  railMultiWorkspace: false,
  facts: new Map(),
  agents: EMPTY_AGENT_INDEX,
};

/** The adaptive set for one card. Every field absent is a normal, common state. */
export interface RailLabels {
  /** Isolation's branch, in mono — also the existing "has unmerged work" flag. */
  branch?: string;
  workspace?: string;
  /** Home-collapsed directory, shown only when it isn't the workspace's primary repo. */
  cwd?: string;
  /** Retro sessions read differently; they get a prefix glyph rather than a word. */
  retro: boolean;
}

/**
 * Whether a cwd names a place at all. An isolation:current_dir session stores
 * cwd "." — the absence of a chosen directory, which displayPath passes
 * through unchanged — and a bare "." on the context line is junk, not a
 * location.
 */
function isLocation(cwd: string | undefined): boolean {
  const trimmed = cwd?.trim();
  return Boolean(trimmed) && trimmed !== "." && trimmed !== "./";
}

/**
 * Whether two paths name the same place, across the forms the records use.
 *
 * A workspace stores its repos as the user typed them, which is usually
 * `~/repo/mads`, while a session's cwd is absolute — the Session Service
 * resolves it with os.Getwd(). Compared raw those never match, so every card
 * would print a directory that is exactly the workspace's primary repo: the
 * redundant label this comparison exists to suppress. displayPath collapses
 * the home prefix and passes an already-collapsed path through unchanged, so
 * running both sides through it puts them in one form.
 */
function samePath(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  const trim = (p: string) => (p.endsWith("/") && p.length > 1 ? p.slice(0, -1) : p);
  return trim(displayPath(a)) === trim(displayPath(b));
}

/**
 * The card's context line (mockup docs/mockups/sessions-ux-v2.html §2, "Labels
 * we already have and don't use"). A 280px card carries about two labels
 * before it stops being scannable, so each field has to earn its place:
 *
 * - branch whenever the session is isolated — the thing most often worth
 *   checking, and the signal that work is sitting unmerged;
 * - workspace only when the visible list spans more than one;
 * - cwd only when it differs from the workspace's primary repo, and never
 *   when it is just the worktree the branch above already names;
 * - a retro glyph for retro sessions.
 *
 * The persona used to take a slot here and no longer does. It was already
 * suppressed whenever it matched the workspace default, so it was a label
 * that is usually absent and, when present, rarely what you are scanning
 * for — while the harness, which the title row now carries, is the thing
 * that actually differs between two cards sitting next to each other. The
 * persona is still in railTooltip, with everything else the resting card
 * drops (model, provider, trust, isolation, context occupancy).
 */
export function railLabelsFor(session: Session, ctx: RailLabelContext): RailLabels {
  const facts = session.workspace ? ctx.facts.get(session.workspace) : undefined;
  // A branch only exists for an isolated session, and an isolated session
  // whose worktree hasn't been provisioned yet has no branch to show.
  const branch = session.worktree_branch || undefined;
  // For worktree isolation cwd IS the worktree dir, so it differs from the
  // primary repo by construction — but the branch is the same fact in a form
  // that fits, so the path would only be spending the card's second slot on a
  // repeat.
  const cwdIsWorktree = samePath(session.cwd, session.worktree_dir);
  const showCwd =
    isLocation(session.cwd) &&
    !samePath(session.cwd, facts?.primaryRepo) &&
    !(branch && cwdIsWorktree);
  return {
    branch,
    workspace: ctx.multiWorkspace ? workspaceLabel(session, ctx.facts) : undefined,
    cwd: showCwd ? displayPath(session.cwd!) : undefined,
    retro: Boolean(session.retro_id) || (session.tags ?? []).includes("retro"),
  };
}

/** Whether this session has anything at all to put on the context line. */
export function hasRailLabels(labels: RailLabels): boolean {
  return Boolean(labels.branch || labels.workspace || labels.cwd || labels.retro);
}

/**
 * Whether THIS card gets a context line at all — asked per card, not per rail.
 *
 * The line's height is reserved whether or not it has content, so that a label
 * appearing or vanishing repaints rather than reflows the rail. But only one
 * label is adaptive: `workspace` comes and goes as a filter narrows the
 * visible set. Everything else (branch, cwd, retro) is a fact about the
 * session that no filter can change. So a card with no label under the
 * rail-wide context can never acquire one, and reserving its line buys nothing
 * — it just spends 17px of every card on blank.
 *
 * That is the whitespace the rail used to carry: the reservation was decided
 * once for the whole rail, so a single session with a branch gave every other
 * card an empty line. Deciding it per card keeps the no-reflow guarantee
 * exactly (the only mover is still rail-wide) and spends the height only where
 * something is written.
 */
export function reservesContextLine(session: Session, ctx: RailLabelContext): boolean {
  return hasRailLabels(railLabelsFor(session, { ...ctx, multiWorkspace: ctx.railMultiWorkspace }));
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
 * the resting card adaptively drops — a card that stopped showing its
 * workspace because the list narrowed must still be able to answer for it.
 */
export function railTooltip(session: Session, ctx: RailLabelContext = EMPTY_LABEL_CONTEXT): string {
  const occupancy = contextOccupancy(session.last_usage, contextWindowForModel(session.model));
  return [
    ...line("Workspace", workspaceLabel(session, ctx.facts)),
    // Through isLocation, exactly as the card is: an isolation:current_dir
    // session stores "." — the absence of a chosen directory — and "Directory:
    // ." is not an answer to "where is this running". The card has always
    // dropped it; the tooltip printed it on every session in the rail.
    ...line("Directory", isLocation(session.cwd) ? displayPath(session.cwd!) : undefined),
    ...line("Branch", session.worktree_branch),
    ...line("Agent", agentNameForRef(session.agent, ctx.agents) ?? session.agent),
    ...line("Model", session.model),
    ...line("Provider", session.provider),
    ...line("Trust", session.trust),
    ...line("Isolation", session.isolation),
    ...line(
      "Context",
      occupancy ? `${occupancy.pct}% of ${Math.round(occupancy.window / 1000)}k` : undefined,
    ),
  ].join("\n");
}
