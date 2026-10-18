import { describe, expect, it } from "vitest";
import { buildRailRows, computeRailLinks, railDescendants } from "./railLinks";
import type { RailRow } from "./railLinks";
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
    const { holders, holdersOf, linkCount } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
      makeLink({ id: "l2", granting_session_id: "lead", subject_session_id: "b", grants: ["read", "inject", "orchestrate"] }),
    ]);
    expect(holders.get("lead")?.badge).toBe("LEADER · 2");
    expect(holders.get("lead")?.subjects).toEqual([
      {
        sessionId: "a",
        badge: "LINKED",
        linkId: "l1",
        cadence: "per_turn",
        grants: ["read", "inject", "orchestrate"],
        grantingSessionId: "lead",
        createdAt: "2026-07-01T00:00:00Z",
      },
      {
        sessionId: "b",
        badge: "LINKED",
        linkId: "l2",
        cadence: "per_turn",
        grants: ["read", "inject", "orchestrate"],
        grantingSessionId: "lead",
        createdAt: "2026-07-01T00:00:00Z",
      },
    ]);
    expect([...holdersOf.keys()]).toEqual(["a", "b"]);
    expect(linkCount.get("lead")).toBe(2);
    expect(linkCount.get("a")).toBe(1);
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
    expect(holders.get("mon")?.subjects).toEqual([
      { sessionId: "a", badge: "WATCHED", linkId: "l1", cadence: "per_turn", grants: ["read"], grantingSessionId: "mon", createdAt: "2026-07-01T00:00:00Z" },
    ]);
  });

  it("prefers the LEADER badge over MONITOR when a session holds both roles", () => {
    const { holders } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "s", subject_session_id: "a", grants: ["read", "inject", "orchestrate"] }),
      makeLink({ id: "l2", granting_session_id: "s", subject_session_id: "b", grants: ["read"] }),
    ]);
    expect(holders.get("s")?.badge).toBe("LEADER");
    expect(holders.get("s")?.subjects).toEqual([
      {
        sessionId: "a",
        badge: "LINKED",
        linkId: "l1",
        cadence: "per_turn",
        grants: ["read", "inject", "orchestrate"],
        grantingSessionId: "s",
        createdAt: "2026-07-01T00:00:00Z",
      },
      {
        sessionId: "b",
        badge: "WATCHED",
        linkId: "l2",
        cadence: "per_turn",
        grants: ["read"],
        grantingSessionId: "s",
        createdAt: "2026-07-01T00:00:00Z",
      },
    ]);
  });

  it("excludes hand-raiser links ({read,inject}, no orchestrate) entirely", () => {
    const { holders, holdersOf, linkCount } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "s", subject_session_id: "s", grants: ["read", "inject"], implicit: true }),
      makeLink({ id: "l2", granting_session_id: "m", subject_session_id: "s", grants: ["read", "inject"], implicit: true }),
    ]);
    expect(holders.size).toBe(0);
    expect(holdersOf.size).toBe(0);
    expect(linkCount.size).toBe(0);
  });

  it("carries a leader link's orchestrate_status through to its subject row", () => {
    const { holders } = computeRailLinks([
      makeLink({
        id: "l1",
        granting_session_id: "lead",
        subject_session_id: "a",
        grants: ["read", "inject", "orchestrate"],
        orchestrate_status: "pending_restart",
      }),
    ]);
    expect(holders.get("lead")?.subjects).toEqual([
      {
        sessionId: "a",
        badge: "LINKED",
        linkId: "l1",
        orchestrateStatus: "pending_restart",
        cadence: "per_turn",
        grants: ["read", "inject", "orchestrate"],
        grantingSessionId: "lead",
        createdAt: "2026-07-01T00:00:00Z",
      },
    ]);
  });

  it("leaves orchestrateStatus undefined on a monitor (WATCHED) row", () => {
    const { holders } = computeRailLinks([
      makeLink({ id: "l1", granting_session_id: "mon", subject_session_id: "a", grants: ["read"] }),
    ]);
    expect(holders.get("mon")?.subjects[0].orchestrateStatus).toBeUndefined();
  });
});

const LEAD: Link["grants"] = ["read", "inject", "orchestrate"];
const WATCH: Link["grants"] = ["read"];

/** The forest as names: "a" for a card, "a > b" for nesting, "a ~> b" for a shortcut under a. */
function shape(rows: RailRow[]): string[] {
  const out: string[] = [];
  const walk = (row: RailRow, path: string) => {
    const here = path ? `${path} > ${row.session.id}` : row.session.id;
    out.push(here);
    for (const sub of row.subjects) walk(sub, here);
    for (const sc of row.shortcuts) out.push(`${here} ~> ${sc.session.id}`);
  };
  for (const row of rows) walk(row, "");
  return out;
}

function rowsFor(sessions: Session[], links: Link[], ordered: Session[] = sessions): RailRow[] {
  return buildRailRows(ordered, new Map(sessions.map((s) => [s.id, s])), computeRailLinks(links));
}

const ids = (...names: string[]) => names.map((id) => makeSession({ id, name: id }));

describe("buildRailRows", () => {
  it("pulls subject sessions out of the flat list and nests them under their holder", () => {
    const lead = makeSession({ id: "lead", name: "investigator" });
    const a = makeSession({ id: "a", name: "payments refactor" });
    const other = makeSession({ id: "other", name: "Maintenance" });
    const rows = rowsFor(
      [lead, a, other],
      [makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: LEAD })],
    );

    expect(rows.map((r) => r.session.id)).toEqual(["lead", "other"]);
    expect(rows[0].badge).toBe("LEADER");
    expect(rows[0].linkCount).toBe(1);
    expect(rows[0].subjects).toEqual([
      {
        session: a,
        badge: undefined,
        link: {
          badge: "LINKED",
          linkId: "l1",
          orchestrateStatus: undefined,
          orchestrateError: undefined,
          cadence: "per_turn",
          grants: LEAD,
          grantingSessionId: "lead",
        },
        linkCount: 1,
        subjects: [],
        shortcuts: [],
      },
    ]);
    expect(rows[1].badge).toBeUndefined();
    expect(rows[1].link).toBeUndefined();
    expect(rows[1].linkCount).toBe(0);
    expect(rows[1].subjects).toEqual([]);
  });

  it("renders every session of a 3-level chain, each nested under its leader", () => {
    // Captain leads Deploy; Deploy leads Rail and Merge. The old one-level
    // nesting drew Deploy under Captain and dropped Rail and Merge entirely.
    const sessions = ids("captain", "deploy", "rail", "merge");
    const rows = rowsFor(sessions, [
      makeLink({ id: "l1", granting_session_id: "captain", subject_session_id: "deploy", grants: LEAD }),
      makeLink({ id: "l2", granting_session_id: "deploy", subject_session_id: "rail", grants: LEAD }),
      makeLink({ id: "l3", granting_session_id: "deploy", subject_session_id: "merge", grants: LEAD }),
    ]);
    expect(shape(rows)).toEqual([
      "captain",
      "captain > deploy",
      "captain > deploy > rail",
      "captain > deploy > merge",
    ]);
    expect(railDescendants(rows[0]).map((s) => s.id)).toEqual(["deploy", "rail", "merge"]);
    // Deploy is both a subject and a holder.
    expect(rows[0].subjects[0].badge).toBe("LEADER · 2");
    expect(rows[0].subjects[0].linkCount).toBe(3);
  });

  it("homes a subject with a leader and a monitor under the leader, with a shortcut under the monitor", () => {
    const sessions = ids("mon", "lead", "s");
    const rows = rowsFor(sessions, [
      // The monitor's link is older; the leader still wins.
      makeLink({ id: "m1", granting_session_id: "mon", subject_session_id: "s", grants: WATCH, created_at: "2026-01-01T00:00:00Z" }),
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "s", grants: LEAD, created_at: "2026-02-01T00:00:00Z" }),
    ]);
    expect(shape(rows)).toEqual(["mon", "mon ~> s", "lead", "lead > s"]);
    const sc = rows[0].shortcuts[0];
    expect(sc.role).toBe("monitor");
    expect(sc.linkId).toBe("m1");
    expect(sc.homeHolder?.id).toBe("lead");
    // Shortcuts are not sessions: the monitor's group is just itself.
    expect(railDescendants(rows[0])).toEqual([]);
    expect(rows[1].subjects[0].linkCount).toBe(2);
  });

  it("homes a subject with only monitors under the earliest link, link id breaking a tie", () => {
    const sessions = ids("late", "early", "tieB", "tieA", "s", "t");
    const rows = rowsFor(sessions, [
      makeLink({ id: "m1", granting_session_id: "late", subject_session_id: "s", grants: WATCH, created_at: "2026-03-01T00:00:00Z" }),
      makeLink({ id: "m2", granting_session_id: "early", subject_session_id: "s", grants: WATCH, created_at: "2026-01-01T00:00:00Z" }),
      makeLink({ id: "z", granting_session_id: "tieB", subject_session_id: "t", grants: WATCH, created_at: "2026-01-01T00:00:00Z" }),
      makeLink({ id: "a", granting_session_id: "tieA", subject_session_id: "t", grants: WATCH, created_at: "2026-01-01T00:00:00Z" }),
    ]);
    expect(shape(rows)).toEqual([
      "late",
      "late ~> s",
      "early",
      "early > s",
      "tieB",
      "tieB ~> t",
      "tieA",
      "tieA > t",
    ]);
  });

  it("drops no session from a monitor cycle, breaking it at the earliest in rail order", () => {
    // a monitors b, b monitors c, c monitors a: nobody has a root above them.
    const sessions = ids("b", "a", "c");
    const rows = rowsFor(sessions, [
      makeLink({ id: "ab", granting_session_id: "a", subject_session_id: "b", grants: WATCH }),
      makeLink({ id: "bc", granting_session_id: "b", subject_session_id: "c", grants: WATCH }),
      makeLink({ id: "ca", granting_session_id: "c", subject_session_id: "a", grants: WATCH }),
    ]);
    // b is first in rail order, so it is the root; c's link to a is drawn
    // in full, and a's link back to b becomes a shortcut.
    expect(shape(rows)).toEqual(["b", "b > c", "b > c > a", "b > c > a ~> b"]);
  });

  it("breaks a legacy leader loop without cutting a session that merely hangs off it", () => {
    // x leads y, y leads x (a loop); y also leads tail. tail comes first in
    // rail order but is not on the loop, so it stays under y.
    const sessions = ids("tail", "y", "x");
    const rows = rowsFor(sessions, [
      makeLink({ id: "xy", granting_session_id: "x", subject_session_id: "y", grants: LEAD }),
      makeLink({ id: "yx", granting_session_id: "y", subject_session_id: "x", grants: LEAD }),
      makeLink({ id: "yt", granting_session_id: "y", subject_session_id: "tail", grants: LEAD }),
    ]);
    expect(shape(rows)).toEqual(["y", "y > x", "y > x ~> y", "y > tail"]);
  });

  it("makes a subject a root when its home holder is not in the list being drawn", () => {
    const lead = makeSession({ id: "lead" });
    const mon = makeSession({ id: "mon" });
    const a = makeSession({ id: "a" });
    const links = [
      makeLink({ id: "l1", granting_session_id: "lead", subject_session_id: "a", grants: LEAD }),
      makeLink({ id: "m1", granting_session_id: "mon", subject_session_id: "a", grants: WATCH }),
    ];
    // The leader filtered out: a is a root, and the monitor still points at it.
    const rows = rowsFor([lead, mon, a], links, [mon, a]);
    expect(shape(rows)).toEqual(["mon", "mon ~> a", "a"]);
    expect(rows[0].shortcuts[0].homeHolder).toBeUndefined();
    // A holder that holds nothing in the list draws no shortcut either.
    expect(shape(rowsFor([lead, a], links, [lead]))).toEqual(["lead"]);
  });
});
