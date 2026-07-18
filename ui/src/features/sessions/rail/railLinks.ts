import type { Link, Session } from "../../../router/types";

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

export interface RailSubjectRow {
  sessionId: string;
  badge: SubjectBadge;
  /** The underlying link's id — Unlink (design decision D6) deletes this via the L1 route. */
  linkId: string;
}

export interface RailHolderInfo {
  /** e.g. "LEADER", "LEADER · 2", "MONITOR", "MONITOR · 2". */
  badge: string;
  subjects: RailSubjectRow[];
}

export interface RailLinkGraph {
  /** Keyed by granting session id — sessions that hold a Leader and/or Monitor role over at least one subject. */
  holders: Map<string, RailHolderInfo>;
  /** Session ids that are a link's subject — pulled out of the flat rail list and rendered nested under their holder instead. */
  nestedSubjectIds: Set<string>;
}

/**
 * Builds the rail's whole-graph relationship view from a flat link list
 * (docs/linked-sessions-mockup.html panel 2/3). A session's own badge
 * reflects Leader role if it holds any (Leader is a superset of Monitor,
 * design decision D3), but subject rows for both a held Leader and a held
 * Monitor relationship are still nested underneath it — only the summary
 * badge collapses to Leader-only.
 */
export function computeRailLinks(links: Link[]): RailLinkGraph {
  const byGranter = new Map<string, Link[]>();
  for (const link of links) {
    const role = linkRole(link);
    if (role === undefined || role === "hand_raiser") continue;
    const existing = byGranter.get(link.granting_session_id);
    if (existing) existing.push(link);
    else byGranter.set(link.granting_session_id, [link]);
  }

  const holders = new Map<string, RailHolderInfo>();
  const nestedSubjectIds = new Set<string>();

  for (const [granterId, granterLinks] of byGranter) {
    const leaderLinkBySubjectId = new Map<string, string>();
    for (const l of granterLinks) {
      if (linkRole(l) === "leader") leaderLinkBySubjectId.set(l.subject_session_id, l.id);
    }
    const monitorLinkBySubjectId = new Map<string, string>();
    for (const l of granterLinks) {
      if (linkRole(l) === "monitor" && !leaderLinkBySubjectId.has(l.subject_session_id)) {
        monitorLinkBySubjectId.set(l.subject_session_id, l.id);
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
      ...[...leaderLinkBySubjectId].map(([sessionId, linkId]) => ({
        sessionId,
        linkId,
        badge: "LINKED" as const,
      })),
      ...[...monitorLinkBySubjectId].map(([sessionId, linkId]) => ({
        sessionId,
        linkId,
        badge: "WATCHED" as const,
      })),
    ];

    holders.set(granterId, { badge, subjects });
    for (const s of subjects) nestedSubjectIds.add(s.sessionId);
  }

  return { holders, nestedSubjectIds };
}

export interface RailRow {
  session: Session;
  badge?: string;
  subjects: { session: Session; badge: SubjectBadge; linkId: string }[];
}

/**
 * Applies a `RailLinkGraph` to one rail group's session list (e.g. `active`
 * or `recent` from `groupSessions`): subject sessions are dropped from the
 * flat list (they render nested under their holder instead, even if the
 * holder falls in a different group) and holder rows gain their badge +
 * resolved subject rows.
 */
export function buildRailRows(
  groupSessions: Session[],
  allSessionsById: Map<string, Session>,
  graph: RailLinkGraph,
): RailRow[] {
  return groupSessions
    .filter((s) => !graph.nestedSubjectIds.has(s.id))
    .map((session) => {
      const holder = graph.holders.get(session.id);
      const subjects = (holder?.subjects ?? []).flatMap((sub) => {
        const subSession = allSessionsById.get(sub.sessionId);
        return subSession
          ? [{ session: subSession, badge: sub.badge, linkId: sub.linkId }]
          : [];
      });
      return { session, badge: holder?.badge, subjects };
    });
}
