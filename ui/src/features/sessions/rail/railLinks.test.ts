import { describe, expect, it } from "vitest";
import { buildRailRows, computeRailLinks } from "./railLinks";
import type { Link, Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function makeLink(overrides: Partial<Link>): Link {
  return {
    id: "link-1",
    granting_session_id: "granter",
    subject_session_id: "subject",
    grants: ["read"],
    cadence: "per_turn",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("computeRailLinks", () => {
  it("badges a leader with a count when it holds 2+ leader links", () => {
    const { holders, nestedSubjectIds } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
      makeLink({ id: "l2", granting_session_id: "lead", subject_session_id: "b", grants: ["read", "inject", "orchestrate"] }),
    ]);
    expect(holders.get("lead")?.badge).toBe("LEADER · 2");
    expect(holders.get("lead")?.subjects).toEqual([
      { sessionId: "a", badge: "LINKED", linkId: "l1" },
      { sessionId: "b", badge: "LINKED", linkId: "l2" },
    ]);
    expect(nestedSubjectIds).toEqual(new Set(["a", "b"]));
  });

  it("elides the count for a single subject", () => {
    const { holders } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
    ]);
    expect(holders.get("lead")?.badge).toBe("LEADER");
  });

  it("badges a monitor without a count for a single watched subject", () => {
    const { holders } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "mon", subject_session_id: "a", grants: ["read"] }),
    ]);
    expect(holders.get("mon")?.badge).toBe("MONITOR");
    expect(holders.get("mon")?.subjects).toEqual([{ sessionId: "a", badge: "WATCHED", linkId: "l1" }]);
  });

  it("prefers the LEADER badge over MONITOR when a session holds both roles", () => {
    const { holders } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "s", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
      makeLink({ id: "l2", granting_session_id: "s", subject_session_id: "b", grants: ["read"] }),
    ]);
    expect(holders.get("s")?.badge).toBe("LEADER");
    expect(holders.get("s")?.subjects).toEqual([
      { sessionId: "a", badge: "LINKED", linkId: "l1" },
      { sessionId: "b", badge: "WATCHED", linkId: "l2" },
    ]);
  });

  it("excludes hand-raiser links ({read,inject}, no orchestrate) entirely", () => {
    const { holders, nestedSubjectIds } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "s", subject_session_id: "s", grants: ["read", "inject"], implicit: true }),
    ]);
    expect(holders.size).toBe(0);
    expect(nestedSubjectIds.size).toBe(0);
  });
});

describe("buildRailRows", () => {
  it("pulls subject sessions out of the flat list and nests them under their holder", () => {
    const lead = makeSession({ id: "lead", name: "investigator" });
    const a = makeSession({ id: "a", name: "payments refactor" });
    const other = makeSession({ id: "other", name: "Maintenance" });
    const graph = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
    ]);
    const allSessionsById = new Map([lead, a, other].map((s) => [s.id, s]));
    const rows = buildRailRows([lead, a, other], allSessionsById, graph);

    expect(rows.map((r) => r.session.id)).toEqual(["lead", "other"]);
    expect(rows[0].badge).toBe("LEADER");
    expect(rows[0].subjects).toEqual([{ session: a, badge: "LINKED", linkId: "l1" }]);
    expect(rows[1].badge).toBeUndefined();
    expect(rows[1].subjects).toEqual([]);
  });

  it("nests a subject under its holder even when the two fall in different groups", () => {
    const lead = makeSession({ id: "lead" });
    const a = makeSession({ id: "a", activity: "inactive" });
    const graph = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: ["read"] }),
    ]);
    const allSessionsById = new Map([lead, a].map((s) => [s.id, s]));
    const activeRows = buildRailRows([lead], allSessionsById, graph);
    const recentRows = buildRailRows([a], allSessionsById, graph);

    expect(activeRows[0].subjects).toEqual([{ session: a, badge: "WATCHED", linkId: "l1" }]);
    expect(recentRows).toEqual([]);
  });
});
