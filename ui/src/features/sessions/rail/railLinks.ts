import type { Grant, Link, LinkCadence, OrchestrateStatus, Session } from "../../../router/types";

export type SubjectBadge = "LINKED" | "WATCHED";

/**
 * Classifies a link's grant set into the rail-relevant role it implies.
 * Presets are creation-time sugar (pkg/contracts/link.go) — storage is
 * always the grant set, so this reads shape, not a stored `preset` field.
 * Hand-raiser ({read,inject}, always Implicit) is system-only and never
 * rendered in the rail (docs/LINKED-SESSIONS-DESIGN.md decision 6).
 */
function linkRole(link: Link): "leader" | "monitor" | "hand_raiser" | undefined {
  if (link.grants.includes("orchestrate")) return "leader";
  if (link.grants.includes("inject")) return "hand_raiser";
  if (link.grants.includes("read")) return "monitor";
  return undefined;
}

/**
 * One counted relationship: a holder's Leader or Monitor link to one
 * subject. A holder with several counted links to the same subject collapses
 * to one relationship, Leader winning (Leader is a superset of Monitor,
 * design decision D3).
 */
export interface RailSubjectRow {
  sessionId: string;
  badge: SubjectBadge;
  /** The underlying link's id — Unlink (design decision D6) deletes this via the L1 route. */
  linkId: string;
  /** Only set for LINKED (leader) rows — the orchestrate grant's materialization state (pkg/contracts/link.go OrchestrateStatus). */
  orchestrateStatus?: OrchestrateStatus;
  /** Why the tools failed to start, when orchestrateStatus is "failed". */
  orchestrateError?: string;
  cadence: LinkCadence;
  /** The underlying link's current grant set — a cadence-only PUT must resend it (store.Links.Update replaces the whole set). */
  grants: Grant[];
  grantingSessionId: string;
  /** The link's creation time — orders a subject's monitors when picking its home. */
  createdAt: string;
}

export interface RailHolderInfo {
  /** e.g. "LEADER", "LEADER · 2", "MONITOR", "MONITOR · 2". */
  badge: string;
  /** Every counted relationship this session holds — leaders first, then monitors, each in link order. */
  subjects: RailSubjectRow[];
}

export interface RailLinkGraph {
  /** Keyed by granting session id — sessions that hold a Leader and/or Monitor role over at least one subject. */
  holders: Map<string, RailHolderInfo>;
  /** Keyed by subject session id — every counted relationship that points at it. */
  holdersOf: Map<string, RailSubjectRow[]>;
  /** Counted relationships touching each session, as holder or subject — the chain glyph's count. */
  linkCount: Map<string, number>;
}

function byCreation(a: RailSubjectRow, b: RailSubjectRow): number {
  if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
  return a.linkId < b.linkId ? -1 : a.linkId > b.linkId ? 1 : 0;
}

/**
 * Builds the rail's whole-graph relationship view from a flat link list
 * (docs/linked-sessions-mockup.html panel 2/3). A session's own badge
 * reflects Leader role if it holds any (Leader is a superset of Monitor,
 * design decision D3), but relationships for both a held Leader and a held
 * Monitor link are still listed — only the summary badge collapses to
 * Leader-only.
 */
export function computeRailLinks(links: Link[]): RailLinkGraph {
  const byGranter = new Map<string, Link[]>();
  for (const link of links) {
    const role = linkRole(link);
    if (role === undefined || role === "hand_raiser") continue;
    // A session never sits under itself; a self-link has nothing to draw.
    if (link.granting_session_id === link.subject_session_id) continue;
    const existing = byGranter.get(link.granting_session_id);
    if (existing) existing.push(link);
    else byGranter.set(link.granting_session_id, [link]);
  }

  const holders = new Map<string, RailHolderInfo>();
  const holdersOf = new Map<string, RailSubjectRow[]>();
  const linkCount = new Map<string, number>();
  const bump = (id: string) => linkCount.set(id, (linkCount.get(id) ?? 0) + 1);

  for (const [granterId, granterLinks] of byGranter) {
    const leaderLinkBySubjectId = new Map<string, Link>();
    for (const l of granterLinks) {
      if (linkRole(l) === "leader") leaderLinkBySubjectId.set(l.subject_session_id, l);
    }
    const monitorLinkBySubjectId = new Map<string, Link>();
    for (const l of granterLinks) {
      if (linkRole(l) === "monitor" && !leaderLinkBySubjectId.has(l.subject_session_id)) {
        monitorLinkBySubjectId.set(l.subject_session_id, l);
      }
    }

    const badge =
      leaderLinkBySubjectId.size > 0
        ? leaderLinkBySubjectId.size > 1
          ? `LEADER · ${leaderLinkBySubjectId.size}`
          : "LEADER"
        : monitorLinkBySubjectId.size > 1
          ? `MONITOR · ${monitorLinkBySubjectId.size}`
          : "MONITOR";

    const subjects: RailSubjectRow[] = [
      ...[...leaderLinkBySubjectId].map(([sessionId, link]) => ({
        sessionId,
        linkId: link.id,
        badge: "LINKED" as const,
        orchestrateStatus: link.orchestrate_status,
        orchestrateError: link.orchestrate_error,
        cadence: link.cadence,
        grants: link.grants,
        grantingSessionId: link.granting_session_id,
        createdAt: link.created_at,
      })),
      ...[...monitorLinkBySubjectId].map(([sessionId, link]) => ({
        sessionId,
        linkId: link.id,
        badge: "WATCHED" as const,
        cadence: link.cadence,
        grants: link.grants,
        grantingSessionId: link.granting_session_id,
        createdAt: link.created_at,
      })),
    ];

    holders.set(granterId, { badge, subjects });
    for (const s of subjects) {
      const list = holdersOf.get(s.sessionId);
      if (list) list.push(s);
      else holdersOf.set(s.sessionId, [s]);
      bump(granterId);
      bump(s.sessionId);
    }
  }

  return { holders, holdersOf, linkCount };
}

/**
 * The relationship a subject is drawn under — its home. Its leader if it has
 * one (at most one is legal; a legacy duplicate falls back to the earliest),
 * otherwise its earliest monitor by link creation, link id breaking a tie.
 */
export function homeLinkOf(graph: RailLinkGraph, sessionId: string): RailSubjectRow | undefined {
  const rels = graph.holdersOf.get(sessionId);
  if (!rels || rels.length === 0) return undefined;
  const leaders = rels.filter((r) => r.badge === "LINKED");
  const pool = leaders.length > 0 ? leaders : rels;
  return [...pool].sort(byCreation)[0];
}

/** How a nested row hangs from its home holder — the link the connector draws. */
export interface RailLinkEdge {
  badge: SubjectBadge;
  linkId: string;
  orchestrateStatus?: OrchestrateStatus;
  orchestrateError?: string;
  cadence: LinkCadence;
  grants: Grant[];
  grantingSessionId: string;
}

/**
 * A one-line pointer under a holder at a subject whose full card lives
 * somewhere else (under its home holder, or as a root). Not a session row:
 * no status, no actions, never counted.
 */
export interface RailShortcut {
  session: Session;
  role: "leader" | "monitor";
  linkId: string;
  /** Where the real card is drawn; undefined when it is a root. */
  homeHolder?: Session;
}

/**
 * One card in the rail and everything that hangs from it. Roots are what
 * `buildRailRows` returns; `subjects` recurse, so a chain of any depth is
 * drawn in full. Every session in the ordered list appears exactly once as a
 * card somewhere in the forest.
 */
export interface RailRow {
  session: Session;
  /** Holder summary ("LEADER", "MONITOR · 2"), when this session holds any counted link. */
  badge?: string;
  /** How this row hangs from its home holder; undefined on a root. */
  link?: RailLinkEdge;
  /** Counted links touching this session as holder or subject (the chain glyph's count). */
  linkCount: number;
  /** Sessions whose home is this one, each with its own subtree. */
  subjects: RailRow[];
  /** Subjects this one holds whose card is drawn elsewhere, after `subjects`. */
  shortcuts: RailShortcut[];
}

/** Every session drawn under `row` as a full card, recursively — never shortcuts. */
export function railDescendants(row: RailRow): Session[] {
  const out: Session[] = [];
  const walk = (r: RailRow) => {
    for (const sub of r.subjects) {
      out.push(sub.session);
      walk(sub);
    }
  };
  walk(row);
  return out;
}

/**
 * Lays the rail's ordered session list out as a forest:
 *
 * - each session lives in exactly one place — under its home holder
 *   (homeLinkOf), or as a root;
 * - a session whose home holder is not in `orderedSessions` (filtered out,
 *   or in a list that isn't drawn) is a root rather than dropped;
 * - a cycle (monitor loops are legal; legacy leader loops may exist) leaves
 *   members with no root above them: the earliest of those, in rail order,
 *   becomes a root, until every session is reachable;
 * - every other holder of a session gets a shortcut row pointing at it.
 */
export function buildRailRows(
  orderedSessions: Session[],
  allSessionsById: Map<string, Session>,
  graph: RailLinkGraph,
): RailRow[] {
  const present = new Map(orderedSessions.map((s) => [s.id, s]));

  const home = new Map<string, RailSubjectRow>();
  for (const s of orderedSessions) {
    const h = homeLinkOf(graph, s.id);
    if (h && present.has(h.grantingSessionId)) home.set(s.id, h);
  }

  // Reachability from the roots, breaking cycles until nothing is stranded.
  const childrenOf = new Map<string, string[]>();
  for (const [id, h] of home) {
    const list = childrenOf.get(h.grantingSessionId);
    if (list) list.push(id);
    else childrenOf.set(h.grantingSessionId, [id]);
  }
  const reached = new Set<string>();
  const reach = (from: string) => {
    const stack = [from];
    while (stack.length > 0) {
      const id = stack.pop()!;
      if (reached.has(id)) continue;
      reached.add(id);
      for (const child of childrenOf.get(id) ?? []) stack.push(child);
    }
  };
  for (const s of orderedSessions) if (!home.has(s.id)) reach(s.id);
  const railIndex = new Map(orderedSessions.map((s, i) => [s.id, i]));
  for (const s of orderedSessions) {
    if (reached.has(s.id)) continue;
    // Stranded: walking up its homes never reaches a root, so the walk ends
    // in a loop. Only a loop member is cut loose — a session merely hanging
    // off the loop stays under its holder — and the earliest in rail order,
    // so the same graph always breaks the same way.
    const path: string[] = [];
    const seen = new Map<string, number>();
    let at: string | undefined = s.id;
    while (at !== undefined && !seen.has(at)) {
      seen.set(at, path.length);
      path.push(at);
      at = home.get(at)?.grantingSessionId;
    }
    const loop = path.slice(seen.get(at!)!);
    const cut = loop.reduce((a, b) => (railIndex.get(b)! < railIndex.get(a)! ? b : a));
    const h = home.get(cut)!;
    home.delete(cut);
    const siblings = childrenOf.get(h.grantingSessionId)!;
    siblings.splice(siblings.indexOf(cut), 1);
    reach(cut);
  }

  const build = (session: Session, link?: RailLinkEdge): RailRow => {
    const held = graph.holders.get(session.id);
    const subjects: RailRow[] = [];
    const shortcuts: RailShortcut[] = [];
    for (const rel of held?.subjects ?? []) {
      const sub = present.get(rel.sessionId);
      if (!sub) continue;
      const subHome = home.get(rel.sessionId);
      if (subHome?.grantingSessionId === session.id) {
        subjects.push(
          build(sub, {
            badge: rel.badge,
            linkId: rel.linkId,
            orchestrateStatus: rel.orchestrateStatus,
            orchestrateError: rel.orchestrateError,
            cadence: rel.cadence,
            grants: rel.grants,
            grantingSessionId: rel.grantingSessionId,
          }),
        );
      } else {
        shortcuts.push({
          session: sub,
          role: rel.badge === "LINKED" ? "leader" : "monitor",
          linkId: rel.linkId,
          homeHolder: subHome
            ? (present.get(subHome.grantingSessionId) ?? allSessionsById.get(subHome.grantingSessionId))
            : undefined,
        });
      }
    }
    return {
      session,
      badge: held?.badge,
      link,
      linkCount: graph.linkCount.get(session.id) ?? 0,
      subjects,
      shortcuts,
    };
  };

  return orderedSessions.filter((s) => !home.has(s.id)).map((s) => build(s));
}
