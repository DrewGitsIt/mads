import { displayPath } from "../../../files/displayPath";
import { contextOccupancy, contextWindowForModel } from "../../../router/modelDisplay";
import type { Session, Workspace } from "../../../router/types";

/**
 * What the rail knows about a workspace, keyed by NAME — Session.workspace is
 * a free-text name, not an id (router/types.ts), and a workspace holds a list
 * of repos with at most one marked primary.
 */
export interface WorkspaceFacts {
  /** The repo that anchors worktree isolation (contracts/registry.go) — the path a cwd is judged redundant against. */
  primaryRepo?: string;
  /** The workspace's default persona; a session bound to it is running the default, which is not worth a label. */
  defaultAgent?: string;
}

export function workspaceFactsByName(workspaces: Workspace[]): Map<string, WorkspaceFacts> {
  return new Map(
    workspaces.map((ws) => {
      // A single-repo workspace may leave `primary` unset — with one repo
      // there is nothing to disambiguate, and it is still the anchor.
      const repos = ws.repos ?? [];
      const primary = repos.find((r) => r.primary) ?? repos[0];
      return [
        ws.name,
        { primaryRepo: primary?.path, defaultAgent: ws.defaults.agent ?? ws.defaults.persona },
      ];
    }),
  );
}

export interface RailLabelContext {
  /**
   * Whether the *visible* list spans more than one workspace. A workspace name
   * that every card would repeat says nothing, so it only earns the card when
   * the list is mixed — which means this flips as filters narrow the list, and
   * is exactly why the context line's height must not depend on its contents.
   */
  multiWorkspace: boolean;
  facts: Map<string, WorkspaceFacts>;
}

export const EMPTY_LABEL_CONTEXT: RailLabelContext = { multiWorkspace: false, facts: new Map() };

/** The adaptive set for one card. Every field absent is a normal, common state. */
export interface RailLabels {
  /** Isolation's branch, in mono — also the existing "has unmerged work" flag. */
  branch?: string;
  workspace?: string;
  /** Home-collapsed directory, shown only when it isn't the workspace's primary repo. */
  cwd?: string;
  agent?: string;
  /** Retro sessions read differently; they get a prefix glyph rather than a word. */
  retro: boolean;
}

function samePath(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  const trim = (p: string) => (p.endsWith("/") && p.length > 1 ? p.slice(0, -1) : p);
  return trim(a) === trim(b);
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
 * - agent only when it isn't the workspace's default persona;
 * - a retro glyph for retro sessions.
 *
 * Everything else (model, provider, trust, isolation, context occupancy) is
 * railTooltip's job, not the resting card's.
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
    Boolean(session.cwd) && !samePath(session.cwd, facts?.primaryRepo) && !(branch && cwdIsWorktree);
  return {
    branch,
    workspace: ctx.multiWorkspace ? session.workspace || undefined : undefined,
    cwd: showCwd ? displayPath(session.cwd!) : undefined,
    agent: session.agent && session.agent !== facts?.defaultAgent ? session.agent : undefined,
    retro: Boolean(session.retro_id) || (session.tags ?? []).includes("retro"),
  };
}

function line(key: string, value: string | undefined): string[] {
  return value ? [`${key}: ${value}`] : [];
}

/**
 * The hover tooltip: every labelling field on the record, including the ones
 * the resting card adaptively drops — a card that stopped showing its
 * workspace because the list narrowed must still be able to answer for it.
 */
export function railTooltip(session: Session): string {
  const occupancy = contextOccupancy(session.last_usage, contextWindowForModel(session.model));
  return [
    ...line("Workspace", session.workspace),
    ...line("Directory", session.cwd ? displayPath(session.cwd) : undefined),
    ...line("Branch", session.worktree_branch),
    ...line("Agent", session.agent),
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
