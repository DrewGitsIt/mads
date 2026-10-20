import { RouterValidationError, type RouterClient } from "../../../router/client";
import { PRESET_GRANTS, type Link, type LinkCadence, type Session } from "../../../router/types";

/**
 * The link editor's draft and save model (docs/mockups/link-modal-constellation.html).
 *
 * The draft is the user's intent per unordered pair, and only for the pairs
 * they touched. Everything else is read live from the server's links, so a
 * change made elsewhere to a pair the user never touched still shows through.
 * Saving turns the difference into an ordered plan of writes (`plan`) and runs
 * it (`applyLinkDraft`), stopping at the first failure. A retry plans again
 * from fresh server state against the same draft, so the steps that already
 * landed have nothing left to do and drop out.
 */

export type LinkRole = "leader" | "monitor";

/** One link as the editor sees it: holder → subject, a role and a cadence. */
export interface DraftEdge {
  granting: string;
  subject: string;
  role: LinkRole;
  cadence: LinkCadence;
}

/** What the user wants for one pair: a link, or no link at all. */
export type PairIntent = DraftEdge | { removed: true };

/** Pair key (`pairKey`) → intent. Only touched pairs, and only while they differ from the server. */
export type LinkDraft = Readonly<Record<string, PairIntent>>;

export const EMPTY_DRAFT: LinkDraft = {};

/** An unordered pair's key: the two ids sorted, so A~B and B~A are one pair. */
export function pairKey(a: string, b: string): string {
  return a < b ? `${a}~${b}` : `${b}~${a}`;
}

export function isRemoved(intent: PairIntent): intent is { removed: true } {
  return "removed" in intent;
}

/** A server link the editor counts: user-made Leader or Monitor, never a self-link or an implicit hand-raiser. */
export interface ServerEdge extends DraftEdge {
  link: Link;
  /**
   * Other stored links for the same pair (a legacy reverse duplicate or a
   * parallel row). The rules allow one link per pair, so a write to the pair
   * deletes these too.
   */
  extras: Link[];
}

function roleOf(link: Link): LinkRole | undefined {
  if (link.grants.includes("orchestrate")) return "leader";
  // read + inject without orchestrate is the system's hand-raiser.
  if (link.grants.includes("inject")) return undefined;
  if (link.grants.includes("read")) return "monitor";
  return undefined;
}

/** The server's counted links, one per pair: a leader beats a monitor, then the oldest wins. */
export function serverEdges(links: Link[]): Map<string, ServerEdge> {
  const byPair = new Map<string, Link[]>();
  for (const link of links) {
    if (link.implicit) continue;
    if (link.granting_session_id === link.subject_session_id) continue;
    if (!roleOf(link)) continue;
    const key = pairKey(link.granting_session_id, link.subject_session_id);
    const list = byPair.get(key);
    // The cache can hold one link twice (fetched for both endpoints); keep one.
    if (list) {
      if (!list.some((l) => l.id === link.id)) list.push(link);
    } else byPair.set(key, [link]);
  }
  const out = new Map<string, ServerEdge>();
  for (const [key, list] of byPair) {
    const sorted = [...list].sort((a, b) => {
      const ra = roleOf(a) === "leader" ? 0 : 1;
      const rb = roleOf(b) === "leader" ? 0 : 1;
      if (ra !== rb) return ra - rb;
      if (a.created_at !== b.created_at) return a.created_at < b.created_at ? -1 : 1;
      return a.id < b.id ? -1 : 1;
    });
    const [link, ...extras] = sorted;
    out.set(key, {
      granting: link.granting_session_id,
      subject: link.subject_session_id,
      role: roleOf(link)!,
      cadence: link.cadence,
      link,
      extras,
    });
  }
  return out;
}

/** A line in the editor: the effective link for one pair. */
export interface EffectiveEdge extends DraftEdge {
  key: string;
  /** The stored link for this pair, if any. */
  server?: ServerEdge;
  /** Differs from what the server holds — the amber dot. */
  dirty: boolean;
  /** No stored link for this pair yet — the "New link" tag. */
  isNew: boolean;
}

function sameEdge(a: DraftEdge, b: DraftEdge): boolean {
  return a.granting === b.granting && a.subject === b.subject && a.role === b.role && a.cadence === b.cadence;
}

/** Whether an intent asks for exactly what the server already holds. */
function intentMatches(intent: PairIntent, server: ServerEdge | undefined): boolean {
  if (isRemoved(intent)) return server === undefined;
  return server !== undefined && sameEdge(intent, server);
}

/** The server links with the draft applied on top. Untouched pairs are read live. */
export function effectiveEdges(links: Link[], draft: LinkDraft): EffectiveEdge[] {
  const server = serverEdges(links);
  const out: EffectiveEdge[] = [];
  for (const [key, s] of server) {
    if (key in draft) continue;
    out.push({ key, granting: s.granting, subject: s.subject, role: s.role, cadence: s.cadence, server: s, dirty: false, isNew: false });
  }
  for (const [key, intent] of Object.entries(draft)) {
    if (isRemoved(intent)) continue;
    const s = server.get(key);
    out.push({ ...pickEdge(intent), key, server: s, dirty: !intentMatches(intent, s), isNew: s === undefined });
  }
  return out;
}

function pickEdge(e: DraftEdge): DraftEdge {
  return { granting: e.granting, subject: e.subject, role: e.role, cadence: e.cadence };
}

/**
 * Sets one pair's intent. An intent that matches the server drops out of the
 * draft, so undoing an edit by hand leaves nothing unsaved.
 */
export function setIntent(links: Link[], draft: LinkDraft, key: string, intent: PairIntent): LinkDraft {
  const next = { ...draft };
  if (intentMatches(intent, serverEdges(links).get(key))) delete next[key];
  else next[key] = isRemoved(intent) ? { removed: true } : pickEdge(intent);
  return next;
}

function edgeFor(links: Link[], draft: LinkDraft, key: string): EffectiveEdge | undefined {
  return effectiveEdges(links, draft).find((e) => e.key === key);
}

/** The effective leader of `subject`, ignoring the pair `exceptKey`. */
export function leaderOf(edges: EffectiveEdge[], subject: string, exceptKey?: string): EffectiveEdge | undefined {
  return edges.find((e) => e.subject === subject && e.role === "leader" && e.key !== exceptKey);
}

export type AddResult = { draft: LinkDraft; key: string; added: boolean };

/**
 * A rail click while the editor is centred on `focus`: open the existing
 * link's editor, or add focus → picked. A new link leads unless the picked
 * session already has a leader (then it monitors), and wakes on signals.
 */
export function addFromRail(links: Link[], draft: LinkDraft, focus: string, picked: string): AddResult {
  const key = pairKey(focus, picked);
  const edges = effectiveEdges(links, draft);
  if (edges.some((e) => e.key === key)) return { draft, key, added: false };
  const role: LinkRole = leaderOf(edges, picked) ? "monitor" : "leader";
  return {
    draft: setIntent(links, draft, key, { granting: focus, subject: picked, role, cadence: "on_signal" }),
    key,
    added: true,
  };
}

function editEdge(links: Link[], draft: LinkDraft, key: string, fn: (e: DraftEdge) => DraftEdge): LinkDraft {
  const e = edgeFor(links, draft, key);
  if (!e) return draft;
  return setIntent(links, draft, key, fn(pickEdge(e)));
}

export function setRole(links: Link[], draft: LinkDraft, key: string, role: LinkRole): LinkDraft {
  return editEdge(links, draft, key, (e) => ({ ...e, role }));
}

export function setCadence(links: Link[], draft: LinkDraft, key: string, cadence: LinkCadence): LinkDraft {
  return editEdge(links, draft, key, (e) => ({ ...e, cadence }));
}

export function swapDirection(links: Link[], draft: LinkDraft, key: string): LinkDraft {
  return editEdge(links, draft, key, (e) => ({ ...e, granting: e.subject, subject: e.granting }));
}

export function removeEdge(links: Link[], draft: LinkDraft, key: string): LinkDraft {
  return setIntent(links, draft, key, { removed: true });
}

/** Why Swap direction is unavailable, or undefined when it's fine. */
export function swapBlockedReason(
  edges: EffectiveEdge[],
  edge: EffectiveEdge,
  nameOf: (id: string) => string,
): string | undefined {
  if (edge.role !== "leader") return undefined;
  const other = leaderOf(edges, edge.granting, edge.key);
  return other ? `${nameOf(edge.granting)} is already led by ${nameOf(other.granting)}.` : undefined;
}

/**
 * The loop of leaders `edge` closes, as session ids from its holder back to
 * its holder (A, B, …, A), or null. Monitor edges never count: two sessions
 * watching each other is harmless.
 */
export function loopThrough(edges: EffectiveEdge[], edge: DraftEdge): string[] | null {
  if (edge.role !== "leader") return null;
  const stack: [string, string[]][] = [[edge.subject, [edge.granting, edge.subject]]];
  const seen = new Set<string>();
  while (stack.length > 0) {
    const [node, path] = stack.pop()!;
    if (node === edge.granting) return path;
    if (seen.has(node)) continue;
    seen.add(node);
    for (const x of edges) {
      if (x.granting === node && x.role === "leader") stack.push([x.subject, [...path, x.subject]]);
    }
  }
  return null;
}

/** A loop of leaders in the graph, told from the user's own edit when one closes it. */
export function firstLoop(edges: EffectiveEdge[]): string[] | null {
  const dirtyFirst = [...edges.filter((e) => e.dirty), ...edges.filter((e) => !e.dirty)];
  for (const e of dirtyFirst) {
    const loop = loopThrough(edges, e);
    if (loop) return loop;
  }
  return null;
}

/** A subject with two leaders in the effective graph (possible only via a change made elsewhere). */
export function firstDoubleLeader(edges: EffectiveEdge[]): { subject: string; holders: string[] } | null {
  const bySubject = new Map<string, string[]>();
  for (const e of edges) {
    if (e.role !== "leader") continue;
    const list = bySubject.get(e.subject) ?? [];
    list.push(e.granting);
    bySubject.set(e.subject, list);
  }
  for (const [subject, holders] of bySubject) if (holders.length > 1) return { subject, holders };
  return null;
}

/**
 * The server's worktree rule (store/links.go worktreeConflict): two sessions
 * in the same workspace that each have their own isolated worktree can't be
 * linked. Links may otherwise cross workspaces and repos.
 */
export function worktreeConflict(a: Session | undefined, b: Session | undefined): boolean {
  if (!a?.worktree_dir || !b?.worktree_dir) return false;
  if (!a.workspace || a.workspace !== b.workspace) return false;
  return a.worktree_dir !== b.worktree_dir;
}

/** Why `other` can't be linked to `focus` at all, or undefined. */
export function linkBlockedReason(focus: Session | undefined, other: Session | undefined): string | undefined {
  if (!focus || !other) return undefined;
  if (focus.id === other.id) return "A session can't link to itself.";
  if (worktreeConflict(focus, other)) {
    return "Each has its own isolated copy of the repo. Linked sessions must share one.";
  }
  return undefined;
}

/** One unsaved change, for the footer count and the discard list. */
export interface PairChange {
  key: string;
  kind: "link" | "swap" | "edit" | "unlink";
  before?: DraftEdge;
  after?: DraftEdge;
}

/** The draft's real differences from the server: pairs whose intent already holds are not changes. */
export function pairChanges(links: Link[], draft: LinkDraft): PairChange[] {
  const server = serverEdges(links);
  const out: PairChange[] = [];
  for (const [key, intent] of Object.entries(draft)) {
    const s = server.get(key);
    if (intentMatches(intent, s)) continue;
    if (isRemoved(intent)) {
      out.push({ key, kind: "unlink", before: s });
    } else if (!s) {
      out.push({ key, kind: "link", after: intent });
    } else if (s.granting !== intent.granting) {
      out.push({ key, kind: "swap", before: s, after: intent });
    } else {
      out.push({ key, kind: "edit", before: s, after: intent });
    }
  }
  return out;
}

export const ROLE_COPY: Record<LinkRole, { glyph: string; title: string; verb: string }> = {
  leader: { glyph: "♛", title: "Leads", verb: "leads" },
  monitor: { glyph: "◉", title: "Monitors", verb: "monitors" },
};

export const CADENCE_COPY: Record<LinkCadence, { title: string; description: string }> = {
  per_turn: { title: "Follow", description: "Digest after each burst of activity." },
  on_signal: {
    title: "Signals",
    description: "Only when it needs attention: permission, crash, task done or blocked.",
  },
  manual_only: { title: "Muted", description: "Total silence. A summary is still available on request." },
};

export const CADENCES: LinkCadence[] = ["per_turn", "on_signal", "manual_only"];

/** A change in words, for the discard confirm. */
export function describeChange(c: PairChange, nameOf: (id: string) => string): string {
  const a = c.after;
  const b = c.before;
  switch (c.kind) {
    case "link":
      return `Link: ${nameOf(a!.granting)} ${ROLE_COPY[a!.role].verb} ${nameOf(a!.subject)}`;
    case "swap":
      return `Swap: ${nameOf(a!.granting)} now ${ROLE_COPY[a!.role].verb} ${nameOf(a!.subject)}`;
    case "unlink":
      return `Unlink: ${nameOf(b!.granting)} → ${nameOf(b!.subject)}`;
    case "edit": {
      const parts = [
        a!.role !== b!.role ? ROLE_COPY[a!.role].title.toLowerCase() : "",
        a!.cadence !== b!.cadence ? CADENCE_COPY[a!.cadence].title.toLowerCase() : "",
      ].filter(Boolean);
      return `${nameOf(a!.granting)} → ${nameOf(a!.subject)}: ${parts.join(", ")}`;
    }
  }
}

/** One server write. Every op names the pair it serves, so a failure can mark that line. */
export type PlanOp =
  | { kind: "delete"; key: string; link: Link }
  | { kind: "update"; key: string; link: Link; edge: DraftEdge }
  | { kind: "create"; key: string; edge: DraftEdge };

/**
 * The writes that turn the server's links into the draft, ordered so that
 * every prefix is a valid graph: deletes, then updates that drop orchestrate,
 * then the other updates, then creates. Leader edges only ever leave in the
 * first two phases and only arrive in the last two, so no prefix holds a
 * leader the final graph doesn't (no second leader, no loop), and a swap's
 * delete always lands before its reverse create. That's what makes stopping
 * at a failure safe.
 */
export function plan(links: Link[], draft: LinkDraft): PlanOp[] {
  const server = serverEdges(links);
  const deletes: PlanOp[] = [];
  const downgrades: PlanOp[] = [];
  const updates: PlanOp[] = [];
  const creates: PlanOp[] = [];
  for (const [key, intent] of Object.entries(draft)) {
    const s = server.get(key);
    if (intentMatches(intent, s)) continue;
    // Touching a pair settles it to one link: legacy duplicates go first.
    for (const extra of s?.extras ?? []) deletes.push({ kind: "delete", key, link: extra });
    if (isRemoved(intent)) {
      if (s) deletes.push({ kind: "delete", key, link: s.link });
      continue;
    }
    const edge = pickEdge(intent);
    if (!s) {
      creates.push({ kind: "create", key, edge });
    } else if (s.granting !== edge.granting) {
      deletes.push({ kind: "delete", key, link: s.link });
      creates.push({ kind: "create", key, edge });
    } else if (s.role === "leader" && edge.role !== "leader") {
      downgrades.push({ kind: "update", key, link: s.link, edge });
    } else {
      updates.push({ kind: "update", key, link: s.link, edge });
    }
  }
  return [...deletes, ...downgrades, ...updates, ...creates];
}

export interface ApplyFailure {
  op: PlanOp;
  message: string;
  /** The server refused it (a ValidationError); false means transient — worth a plain retry. */
  refused: boolean;
}

export interface ApplyResult {
  appliedCount: number;
  total: number;
  failed?: ApplyFailure;
  /**
   * After a failure: the sessions the plan touched, and their links fetched
   * fresh, so the next plan starts from what the server now holds. Absent if
   * that refetch failed too.
   */
  fresh?: { sessionIds: string[]; links: Link[] };
}

function isNotFound(err: unknown): boolean {
  return err instanceof Error && /\b404\b/.test(err.message);
}

function sentence(message: string): string {
  return message.charAt(0).toUpperCase() + message.slice(1);
}

function failureOf(op: PlanOp, err: unknown): ApplyFailure {
  if (err instanceof RouterValidationError) {
    // The message alone: the field is a wire key, and each refusal is already
    // a sentence about these sessions.
    return { op, refused: true, message: err.errors.map((e) => sentence(e.message)).join(" ") };
  }
  return { op, refused: false, message: err instanceof Error ? err.message : String(err) };
}

function runOp(client: RouterClient, op: PlanOp): Promise<unknown> {
  switch (op.kind) {
    case "delete":
      return client.deleteLink(op.link.id).catch((err: unknown) => {
        // Already gone is what a delete wanted.
        if (!isNotFound(err)) throw err;
      });
    case "update":
      return client.updateLink(op.link.id, {
        granting_session_id: op.edge.granting,
        subject_session_id: op.edge.subject,
        grants: PRESET_GRANTS[op.edge.role],
        cadence: op.edge.cadence,
      });
    case "create":
      return client.createLink({
        granting_session_id: op.edge.granting,
        subject_session_id: op.edge.subject,
        grants: PRESET_GRANTS[op.edge.role],
        cadence: op.edge.cadence,
      });
  }
}

function opSessions(op: PlanOp): string[] {
  return op.kind === "create"
    ? [op.edge.granting, op.edge.subject]
    : [op.link.granting_session_id, op.link.subject_session_id];
}

/** Runs a plan in order, stopping at the first failure. */
export async function applyLinkDraft(client: RouterClient, ops: PlanOp[]): Promise<ApplyResult> {
  for (let i = 0; i < ops.length; i++) {
    try {
      await runOp(client, ops[i]);
    } catch (err) {
      const result: ApplyResult = { appliedCount: i, total: ops.length, failed: failureOf(ops[i], err) };
      const sessionIds = [...new Set(ops.flatMap(opSessions))];
      try {
        const lists = await Promise.all(sessionIds.map((id) => client.getSessionLinks(id)));
        result.fresh = { sessionIds, links: lists.flat() };
      } catch {
        // The live link feed will catch up; the next plan just starts from it.
      }
      return result;
    }
  }
  return { appliedCount: ops.length, total: ops.length };
}

/** `links` with every link touching `sessionIds` replaced by `fresh` — a refetch merged into a cache. */
export function mergeFreshLinks(links: Link[], sessionIds: string[], fresh: Link[]): Link[] {
  const ids = new Set(sessionIds);
  const kept = links.filter((l) => !ids.has(l.granting_session_id) && !ids.has(l.subject_session_id));
  const seen = new Set(kept.map((l) => l.id));
  for (const l of fresh) {
    if (seen.has(l.id)) continue;
    seen.add(l.id);
    kept.push(l);
  }
  return kept;
}
