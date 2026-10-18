import { describe, expect, it, vi } from "vitest";
import { RouterValidationError, type RouterClient } from "../../../router/client";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Link, LinkCadence, Session } from "../../../router/types";
import {
  EMPTY_DRAFT,
  addFromRail,
  applyLinkDraft,
  describeChange,
  effectiveEdges,
  firstDoubleLeader,
  firstLoop,
  leaderOf,
  linkBlockedReason,
  loopThrough,
  mergeFreshLinks,
  pairChanges,
  pairKey,
  plan,
  removeEdge,
  serverEdges,
  setCadence,
  setRole,
  swapBlockedReason,
  swapDirection,
  type LinkDraft,
  type PlanOp,
} from "./linkDraft";

const LEAD = ["read", "inject", "orchestrate"] as Link["grants"];
const MON = ["read"] as Link["grants"];

let seq = 0;
function link(h: string, s: string, role: "leader" | "monitor", cadence: LinkCadence = "on_signal", over: Partial<Link> = {}): Link {
  seq += 1;
  return {
    id: `l-${h}-${s}`,
    granting_session_id: h,
    subject_session_id: s,
    grants: role === "leader" ? LEAD : MON,
    cadence,
    created_at: `2026-10-01T00:00:${String(seq).padStart(2, "0")}Z`,
    updated_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

const names: Record<string, string> = { p: "Captain", a: "Deploy", b: "Rail", c: "Merge", m: "Watchdog", x: "Solo" };
const nameOf = (id: string) => names[id] ?? id;

/** p leads a; m monitors a; a leads b and c. */
function seed(): Link[] {
  return [link("p", "a", "leader"), link("m", "a", "monitor", "per_turn"), link("a", "b", "leader"), link("a", "c", "leader", "manual_only")];
}

const edgeOf = (links: Link[], draft: LinkDraft, x: string, y: string) =>
  effectiveEdges(links, draft).find((e) => e.key === pairKey(x, y));

describe("serverEdges", () => {
  it("counts only user-made Leader and Monitor links, one per pair", () => {
    const links = [
      link("p", "a", "leader"),
      link("a", "a", "leader"),
      link("q", "q", "monitor", "on_signal", { grants: ["read", "inject"], implicit: true }),
      link("h", "r", "monitor", "on_signal", { grants: ["read", "inject"] }),
    ];
    expect([...serverEdges(links).keys()]).toEqual([pairKey("p", "a")]);
  });

  it("keeps a leader over a duplicate for the same pair, and remembers the extra", () => {
    const mon = link("a", "p", "monitor");
    const lead = link("p", "a", "leader");
    const e = serverEdges([mon, lead]).get(pairKey("a", "p"))!;
    expect(e.link.id).toBe(lead.id);
    expect(e.extras.map((l) => l.id)).toEqual([mon.id]);
  });

  it("ignores the same link listed twice (fetched for both endpoints)", () => {
    const l = link("p", "a", "leader");
    expect(serverEdges([l, { ...l }]).get(pairKey("p", "a"))!.extras).toEqual([]);
  });
});

describe("effective graph: overrides vs untouched server changes", () => {
  it("shows a change made elsewhere to a pair the user never touched", () => {
    const before = seed();
    const draft = setCadence(before, EMPTY_DRAFT, pairKey("a", "b"), "per_turn");
    // Elsewhere: a's link to c goes to Follow, and a brand new monitor appears.
    const after = [...before.filter((l) => l.subject_session_id !== "c"), link("a", "c", "leader", "per_turn"), link("x", "b", "monitor")];
    const edges = effectiveEdges(after, draft);
    expect(edges.find((e) => e.key === pairKey("a", "c"))!.cadence).toBe("per_turn");
    expect(edges.some((e) => e.key === pairKey("x", "b"))).toBe(true);
    // The touched pair keeps the user's intent.
    expect(edges.find((e) => e.key === pairKey("a", "b"))).toMatchObject({ cadence: "per_turn", dirty: true });
  });

  it("keeps the user's intent for a touched pair even when the server changes it", () => {
    const before = seed();
    const draft = setRole(before, EMPTY_DRAFT, pairKey("a", "b"), "monitor");
    const after = before.map((l) => (l.subject_session_id === "b" ? { ...l, cadence: "per_turn" as const } : l));
    expect(edgeOf(after, draft, "a", "b")).toMatchObject({ role: "monitor", cadence: "on_signal", dirty: true });
  });

  it("drops an edit undone by hand, leaving nothing unsaved", () => {
    const links = seed();
    let draft = setCadence(links, EMPTY_DRAFT, pairKey("a", "b"), "per_turn");
    expect(pairChanges(links, draft)).toHaveLength(1);
    draft = setCadence(links, draft, pairKey("a", "b"), "on_signal");
    expect(draft).toEqual({});
  });

  it("stops counting an intent the server has since come to hold", () => {
    const links = seed();
    const draft = setCadence(links, EMPTY_DRAFT, pairKey("a", "b"), "per_turn");
    const landed = links.map((l) => (l.subject_session_id === "b" ? { ...l, cadence: "per_turn" as const } : l));
    expect(pairChanges(landed, draft)).toEqual([]);
    expect(plan(landed, draft)).toEqual([]);
    expect(edgeOf(landed, draft, "a", "b")!.dirty).toBe(false);
  });

  it("hides a removed pair", () => {
    const links = seed();
    const draft = removeEdge(links, EMPTY_DRAFT, pairKey("a", "c"));
    expect(edgeOf(links, draft, "a", "c")).toBeUndefined();
  });
});

describe("addFromRail", () => {
  it("adds focus → picked as Leads with Signals", () => {
    const links = seed();
    const r = addFromRail(links, EMPTY_DRAFT, "a", "x");
    expect(r.added).toBe(true);
    expect(r.key).toBe(pairKey("a", "x"));
    expect(edgeOf(links, r.draft, "a", "x")).toMatchObject({
      granting: "a",
      subject: "x",
      role: "leader",
      cadence: "on_signal",
      isNew: true,
      dirty: true,
    });
  });

  it("monitors instead when the picked session already has a leader", () => {
    const links = seed();
    const r = addFromRail(links, EMPTY_DRAFT, "m", "b");
    expect(edgeOf(links, r.draft, "m", "b")!.role).toBe("monitor");
  });

  it("opens the existing link rather than adding a second, in either direction", () => {
    const links = seed();
    const r = addFromRail(links, EMPTY_DRAFT, "a", "p");
    expect(r).toEqual({ draft: EMPTY_DRAFT, key: pairKey("a", "p"), added: false });
  });

  it("re-adds a pair removed in the draft as a fresh link from the focus", () => {
    const links = seed();
    const removed = removeEdge(links, EMPTY_DRAFT, pairKey("a", "b"));
    const r = addFromRail(links, removed, "b", "a");
    expect(r.added).toBe(true);
    // a already has a leader (p), so b → a monitors.
    expect(edgeOf(links, r.draft, "b", "a")).toMatchObject({ granting: "b", role: "monitor", isNew: false, dirty: true });
  });
});

describe("validation", () => {
  it("finds the leader that disables Leads, ignoring the edge being edited", () => {
    const edges = effectiveEdges(seed(), EMPTY_DRAFT);
    const mOverA = edges.find((e) => e.key === pairKey("m", "a"))!;
    expect(leaderOf(edges, "a", mOverA.key)!.granting).toBe("p");
    const pOverA = edges.find((e) => e.key === pairKey("p", "a"))!;
    expect(leaderOf(edges, "a", pOverA.key)).toBeUndefined();
  });

  it("blocks a swap that would give the new subject a second leader", () => {
    const links = seed();
    const edges = effectiveEdges(links, EMPTY_DRAFT);
    // a → b swapped makes b lead a, but p already leads a.
    expect(swapBlockedReason(edges, edges.find((e) => e.key === pairKey("a", "b"))!, nameOf)).toBe(
      "Deploy is already led by Captain.",
    );
    // A monitor swaps freely.
    expect(swapBlockedReason(edges, edges.find((e) => e.key === pairKey("m", "a"))!, nameOf)).toBeUndefined();
  });

  it("finds a loop of leaders and spells out its path", () => {
    const links = seed();
    // Centre on Merge, pick Captain: c leads p closes p → a → c → p.
    const r = addFromRail(links, EMPTY_DRAFT, "c", "p");
    const edges = effectiveEdges(links, r.draft);
    expect(loopThrough(edges, edges.find((e) => e.key === r.key)!)).toEqual(["c", "p", "a", "c"]);
    expect(firstLoop(edges)).not.toBeNull();
    // Turning the closing link into a monitor breaks it.
    const fixed = setRole(links, r.draft, r.key, "monitor");
    expect(firstLoop(effectiveEdges(links, fixed))).toBeNull();
  });

  it("never counts monitors toward a loop", () => {
    const links = [link("a", "b", "monitor"), link("b", "c", "monitor"), link("c", "a", "leader")];
    expect(firstLoop(effectiveEdges(links, EMPTY_DRAFT))).toBeNull();
  });

  it("notices a subject with two leaders", () => {
    const links = seed();
    // Draft: x leads b. Elsewhere, nothing; but a already leads b.
    const draft: LinkDraft = { [pairKey("x", "b")]: { granting: "x", subject: "b", role: "leader", cadence: "on_signal" } };
    expect(firstDoubleLeader(effectiveEdges(links, draft))).toEqual({ subject: "b", holders: ["a", "x"] });
  });

  it("mirrors the server's worktree rule, and lets links cross workspaces", () => {
    const s = (id: string, over: Partial<Session>): Session => ({
      id,
      name: id,
      activity: "inactive",
      created_at: "",
      updated_at: "",
      ...over,
    });
    const a = s("a", { workspace: "w1", worktree_dir: "/wt/a" });
    expect(linkBlockedReason(a, s("b", { workspace: "w1", worktree_dir: "/wt/b" }))).toMatch(/isolated copy/);
    expect(linkBlockedReason(a, s("b", { workspace: "w1", worktree_dir: "/wt/a" }))).toBeUndefined();
    expect(linkBlockedReason(a, s("b", { workspace: "w1" }))).toBeUndefined();
    expect(linkBlockedReason(a, s("b", { workspace: "w2", worktree_dir: "/wt/b" }))).toBeUndefined();
    expect(linkBlockedReason(a, a)).toMatch(/itself/);
  });
});

describe("plan", () => {
  const kinds = (ops: PlanOp[]) =>
    ops.map((o) =>
      o.kind === "create"
        ? `create ${o.edge.granting}>${o.edge.subject} ${o.edge.role}`
        : o.kind === "update"
          ? `update ${o.link.id} ${o.edge.role}/${o.edge.cadence}`
          : `delete ${o.link.id}`,
    );

  it("orders deletes, then orchestrate downgrades, then other updates, then creates", () => {
    const links = seed();
    let d: LinkDraft = EMPTY_DRAFT;
    d = addFromRail(links, d, "a", "x").draft; // create
    d = setCadence(links, d, pairKey("m", "a"), "on_signal"); // plain update
    d = setRole(links, d, pairKey("a", "c"), "monitor"); // downgrade
    d = removeEdge(links, d, pairKey("a", "b")); // delete
    expect(kinds(plan(links, d))).toEqual([
      "delete l-a-b",
      "update l-a-c monitor/manual_only",
      "update l-m-a monitor/on_signal",
      "create a>x leader",
    ]);
  });

  it("plans a swap as a delete before the reverse create", () => {
    const links = seed();
    const d = swapDirection(links, EMPTY_DRAFT, pairKey("m", "a"));
    expect(kinds(plan(links, d))).toEqual(["delete l-m-a", "create a>m monitor"]);
    expect(pairChanges(links, d)[0].kind).toBe("swap");
  });

  it("moves a leader from one holder to another without ever holding two", () => {
    const links = seed();
    // m takes over leading a from p: p drops to monitor, m goes up to leader.
    let d = setRole(links, EMPTY_DRAFT, pairKey("p", "a"), "monitor");
    d = setRole(links, d, pairKey("m", "a"), "leader");
    const ops = plan(links, d);
    expect(kinds(ops)).toEqual(["update l-p-a monitor/on_signal", "update l-m-a leader/per_turn"]);
    // Every prefix keeps at most one leader per subject.
    let state = links;
    for (const op of ops) {
      state = state.map((l) =>
        op.kind === "update" && l.id === op.link.id ? { ...l, grants: op.edge.role === "leader" ? LEAD : MON } : l,
      );
      expect(firstDoubleLeader(effectiveEdges(state, EMPTY_DRAFT))).toBeNull();
    }
  });

  it("moves a leader by unlinking the old holder before linking the new one", () => {
    const links = seed();
    let d = removeEdge(links, EMPTY_DRAFT, pairKey("p", "a"));
    d = addFromRail(links, d, "x", "a").draft;
    expect(kinds(plan(links, d))).toEqual(["delete l-p-a", "create x>a leader"]);
  });

  it("sends preset grants and the cadence", async () => {
    const links = seed();
    const client = new FakeRouterClient();
    const update = vi.spyOn(client, "updateLink");
    const create = vi.spyOn(client, "createLink");
    let d = setRole(links, EMPTY_DRAFT, pairKey("m", "a"), "monitor");
    d = setCadence(links, d, pairKey("m", "a"), "manual_only");
    d = addFromRail(links, d, "a", "x").draft;
    await applyLinkDraft(client, plan(links, d));
    expect(update).toHaveBeenCalledWith("l-m-a", {
      granting_session_id: "m",
      subject_session_id: "a",
      grants: ["read"],
      cadence: "manual_only",
    });
    expect(create).toHaveBeenCalledWith({
      granting_session_id: "a",
      subject_session_id: "x",
      grants: ["read", "inject", "orchestrate"],
      cadence: "on_signal",
    });
  });

  it("settles a touched pair to one link, deleting legacy duplicates", () => {
    const extra = link("a", "p", "monitor");
    const links = [...seed(), extra];
    const d = setCadence(links, EMPTY_DRAFT, pairKey("p", "a"), "per_turn");
    expect(kinds(plan(links, d))).toEqual([`delete ${extra.id}`, "update l-p-a leader/per_turn"]);
  });

  it("re-plans after a partial apply, skipping what already landed", () => {
    const links = seed();
    let d = removeEdge(links, EMPTY_DRAFT, pairKey("a", "b"));
    d = setCadence(links, d, pairKey("m", "a"), "on_signal");
    d = addFromRail(links, d, "a", "x").draft;
    expect(plan(links, d)).toHaveLength(3);
    // The delete and the update landed; the create failed.
    const now = links
      .filter((l) => l.id !== "l-a-b")
      .map((l) => (l.id === "l-m-a" ? { ...l, cadence: "on_signal" as const } : l));
    expect(kinds(plan(now, d))).toEqual(["create a>x leader"]);
    expect(pairChanges(now, d)).toHaveLength(1);
  });
});

describe("describeChange", () => {
  it("words each kind of change", () => {
    const links = seed();
    let d = addFromRail(links, EMPTY_DRAFT, "a", "x").draft;
    d = removeEdge(links, d, pairKey("a", "b"));
    d = swapDirection(links, d, pairKey("m", "a"));
    d = setCadence(links, d, pairKey("a", "c"), "per_turn");
    d = setRole(links, d, pairKey("a", "c"), "monitor");
    expect(pairChanges(links, d).map((c) => describeChange(c, nameOf))).toEqual([
      "Link: Deploy leads Solo",
      "Unlink: Deploy → Rail",
      "Swap: Deploy now monitors Watchdog",
      "Deploy → Merge: monitors, follow",
    ]);
  });
});

describe("applyLinkDraft", () => {
  function mockClient(over: Partial<RouterClient>): RouterClient {
    return {
      deleteLink: vi.fn(() => Promise.resolve()),
      updateLink: vi.fn((id: string) => Promise.resolve({ id } as Link)),
      createLink: vi.fn(() => Promise.resolve({ id: "new" } as Link)),
      getSessionLinks: vi.fn(() => Promise.resolve([] as Link[])),
      ...over,
    } as unknown as RouterClient;
  }
  const ops = (): PlanOp[] => {
    const links = seed();
    let d = removeEdge(links, EMPTY_DRAFT, pairKey("a", "b"));
    d = setCadence(links, d, pairKey("m", "a"), "on_signal");
    d = addFromRail(links, d, "a", "x").draft;
    return plan(links, d);
  };

  it("runs every op in order when nothing fails", async () => {
    const client = mockClient({});
    expect(await applyLinkDraft(client, ops())).toEqual({ appliedCount: 3, total: 3 });
    expect(client.getSessionLinks).not.toHaveBeenCalled();
  });

  it("stops at the first failure, marks it transient, and hands back fresh links", async () => {
    const fresh = [link("p", "a", "leader")];
    const client = mockClient({
      updateLink: vi.fn(() => Promise.reject(new Error("router request failed: 502 bad gateway"))),
      getSessionLinks: vi.fn((id: string) => Promise.resolve(id === "a" ? fresh : [])),
    });
    const r = await applyLinkDraft(client, ops());
    expect(r.appliedCount).toBe(1);
    expect(r.total).toBe(3);
    expect(r.failed).toMatchObject({ refused: false, message: "router request failed: 502 bad gateway" });
    expect(r.failed!.op.kind).toBe("update");
    expect(client.createLink).not.toHaveBeenCalled();
    expect(r.fresh!.sessionIds.sort()).toEqual(["a", "b", "m", "x"]);
    expect(r.fresh!.links).toEqual(fresh);
  });

  it("marks a ValidationError as refused, with the server's sentence", async () => {
    const client = mockClient({
      createLink: vi.fn(() =>
        Promise.reject(new RouterValidationError([{ field: "grants", message: "orchestrate is refused: live run" }])),
      ),
    });
    const r = await applyLinkDraft(client, ops());
    expect(r.appliedCount).toBe(2);
    expect(r.failed).toMatchObject({ refused: true, message: "Orchestrate is refused: live run" });
    expect(r.failed!.op.key).toBe(pairKey("a", "x"));
  });

  it("treats a 404 on delete as already done", async () => {
    const client = mockClient({
      deleteLink: vi.fn(() => Promise.reject(new Error("router request failed: 404 not found"))),
    });
    expect(await applyLinkDraft(client, ops())).toEqual({ appliedCount: 3, total: 3 });
  });

  it("still reports the failure when the refetch fails too", async () => {
    const client = mockClient({
      deleteLink: vi.fn(() => Promise.reject(new Error("offline"))),
      getSessionLinks: vi.fn(() => Promise.reject(new Error("offline"))),
    });
    const r = await applyLinkDraft(client, ops());
    expect(r.appliedCount).toBe(0);
    expect(r.fresh).toBeUndefined();
  });
});

describe("mergeFreshLinks", () => {
  it("replaces every link touching the refetched sessions", () => {
    const keep = link("q", "r", "monitor");
    const stale = link("a", "b", "leader");
    const fresh = link("a", "c", "leader");
    expect(mergeFreshLinks([keep, stale], ["a"], [fresh, fresh])).toEqual([keep, fresh]);
  });
});
