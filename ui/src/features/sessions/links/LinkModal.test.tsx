import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { FLAGS } from "../../../featureFlags";
import { RouterValidationError, type RouterClient } from "../../../router/client";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Link, LinkCadence, Session } from "../../../router/types";
import { SessionRail } from "../rail/SessionRail";
import { LinkModal } from "./LinkModal";
import { useLinkEditor, useRailLinkEditor } from "./useLinkEditor";
import { GAP_F, NODE_HALF, NODE_W, PER, SKY_W, layoutSky } from "./skyLayout";

const LEAD = ["read", "inject", "orchestrate"] as Link["grants"];

function session(id: string, name: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name,
    activity: "inactive",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    last_turn_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

function link(h: string, s: string, role: "leader" | "monitor", cadence: LinkCadence = "on_signal"): Link {
  return {
    id: `l-${h}-${s}`,
    granting_session_id: h,
    subject_session_id: s,
    grants: role === "leader" ? LEAD : ["read"],
    cadence,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

const SESSIONS = [
  session("p", "Captain"),
  session("m", "Watchdog"),
  session("a", "Deploy"),
  session("b", "Rail"),
  session("c", "Merge"),
  session("x", "Solo"),
];

/** Captain leads Deploy, Watchdog monitors it; Deploy leads Rail and (muted) Merge. */
const SEED = [
  link("p", "a", "leader"),
  link("m", "a", "monitor", "per_turn"),
  link("a", "b", "leader"),
  link("a", "c", "leader", "manual_only"),
];

function fakeClient(links: Link[] = SEED): FakeRouterClient {
  const u = undefined;
  return new FakeRouterClient(u, u, u, u, u, u, u, u, u, u, links);
}

/** The surface's wiring, minus everything else the surface does. */
function Harness({
  client,
  sessions = SESSIONS,
  links = SEED,
  onSelect,
}: {
  client: RouterClient;
  sessions?: Session[];
  links?: Link[];
  onSelect?: (id: string) => void;
}) {
  const [live] = useState(links);
  const editor = useLinkEditor({ links: live, sessions, client });
  const railEditor = useRailLinkEditor(editor);
  return (
    <>
      <SessionRail
        sessions={sessions}
        openTaskCounts={{}}
        links={live}
        routerClient={client}
        onSelect={onSelect}
        onOpenLinks={(id, trigger) => editor.openOn(id, trigger)}
        linkEditor={railEditor}
      />
      {editor.isOpen && (
        <div data-testid="scrim" onClick={editor.requestClose}>
          <LinkModal editor={editor} />
        </div>
      )}
    </>
  );
}

const rail = () => within(screen.getByLabelText("Session list"));
const dialog = () => screen.getByRole("dialog");
const badge = (from: string, verb: string, to: string) =>
  within(dialog()).getByRole("button", { name: new RegExp(`^${from} ${verb} ${to},`) });
const saveButton = () => within(dialog()).getByRole("button", { name: /^(Save|Retry|No changes|Saving)/ });

function openOn(name: string) {
  fireEvent.click(rail().getByRole("button", { name: `Links for ${name}` }));
}

describe("LinkModal with the rail", () => {
  beforeEach(() => {
    FLAGS.linkedSessions = true;
  });
  afterEach(() => {
    FLAGS.linkedSessions = false;
  });

  it("opens from a card's chain, centred on that session, holders above and subjects below", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    const d = screen.getByRole("dialog", { name: "Links · Deploy" });
    expect(d.getAttribute("aria-modal")).toBe("false");
    expect(document.activeElement).toBe(d);
    // Every other node is a button that re-centres.
    for (const name of ["Captain", "Watchdog", "Rail", "Merge"]) {
      expect(within(d).getByRole("button", { name: new RegExp(`^Center on ${name}`) })).toBeTruthy();
    }
    const top = (name: string) => parseFloat((d.querySelector(`[data-node="${name}"]`) as HTMLElement).style.top);
    expect(top("p")).toBeLessThan(top("a"));
    expect(top("m")).toBeLessThan(top("a"));
    expect(top("b")).toBeGreaterThan(top("a"));
    // Lines carry role and cadence; the muted one wears the mute icon, not a word.
    expect(d.querySelector('[data-wire][data-role="leader"][data-cadence="on_signal"]')).toBeTruthy();
    expect(d.querySelector('[data-wire][data-role="monitor"][data-cadence="per_turn"]')).toBeTruthy();
    expect(badge("Deploy", "leads", "Merge").querySelector("[data-mute-icon]")).toBeTruthy();
    expect(badge("Watchdog", "monitors", "Deploy").textContent).toContain("Follow");
    // The rail is the picker now.
    expect(rail().getByText("Center")).toBeTruthy();
    const card = (name: string) => rail().getByText(name).closest("[data-rail-card]")!;
    expect(card("Rail").getAttribute("data-link-edit")).toBe("linked");
    expect(card("Solo").getAttribute("data-link-edit")).toBe("");
    expect(within(d).getByText("No changes")).toBeTruthy();
  });

  it("counts links outside the view on each node", () => {
    const links = [...SEED, link("x", "b", "monitor")];
    render(<Harness client={fakeClient(links)} links={links} />);
    openOn("Deploy");
    const rail = within(dialog()).getByRole("button", { name: /^Center on Rail/ });
    expect(rail.getAttribute("aria-label")).toBe("Center on Rail (1 more holding it)");
    expect(rail.textContent).toContain("▲1");
  });

  it("adds a link from a rail click, opens its editor, and never opens the session", () => {
    const onSelect = vi.fn();
    render(<Harness client={fakeClient()} onSelect={onSelect} />);
    openOn("Deploy");
    fireEvent.click(rail().getByText("Solo"));
    expect(onSelect).not.toHaveBeenCalled();
    const ed = within(dialog()).getByRole("group", { name: "Edit link Deploy → Solo" });
    expect(within(ed).getByText("New link")).toBeTruthy();
    expect(within(ed).getByRole("button", { name: "♛ Leads" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(ed).getByRole("radio", { name: /Signals/ }).getAttribute("aria-checked")).toBe("true");
    expect(badge("Deploy", "leads", "Solo").hasAttribute("data-dirty")).toBe(true);
    expect(within(dialog()).getByText("1 unsaved")).toBeTruthy();
    expect(saveButton().textContent).toBe("Save 1 change");
    expect(rail().getByText("Solo").closest("[data-rail-card]")!.getAttribute("data-link-edit")).toBe("linked");
    // A second click on a linked session opens its editor instead of adding again.
    fireEvent.click(rail().getByText("Rail"));
    expect(within(dialog()).getByRole("group", { name: "Edit link Deploy → Rail" })).toBeTruthy();
    expect(within(dialog()).getByText("1 unsaved")).toBeTruthy();
  });

  it("adds a monitor when the picked session already has a leader, with Leads disabled and why", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Watchdog");
    fireEvent.click(rail().getByText("Rail"));
    const ed = within(dialog()).getByRole("group", { name: "Edit link Watchdog → Rail" });
    expect(within(ed).getByRole("button", { name: "◉ Monitors" }).getAttribute("aria-pressed")).toBe("true");
    expect((within(ed).getByRole("button", { name: "♛ Leads" }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(ed).getByText("Already led by Deploy.")).toBeTruthy();
  });

  it("disables Swap with the reason when the new subject already has a leader", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    fireEvent.click(badge("Deploy", "leads", "Rail"));
    const ed = within(dialog()).getByRole("group", { name: "Edit link Deploy → Rail" });
    expect((within(ed).getByRole("button", { name: /Swap direction/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(ed).getByText("Deploy is already led by Captain.")).toBeTruthy();
  });

  it("re-centres on a node and steps back through the breadcrumbs", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    fireEvent.click(within(dialog()).getByRole("button", { name: /^Center on Rail/ }));
    expect(screen.getByRole("dialog", { name: "Links · Rail" })).toBeTruthy();
    const crumbs = within(dialog()).getByRole("navigation", { name: "Centered on" });
    expect(within(crumbs).getByText("Rail").getAttribute("aria-current")).toBe("location");
    expect(rail().getByText("Rail").closest("[data-rail-card]")!.getAttribute("data-link-edit")).toBe("center");
    fireEvent.click(within(crumbs).getByRole("button", { name: "Deploy" }));
    expect(screen.getByRole("dialog", { name: "Links · Deploy" })).toBeTruthy();
  });

  it("edits a cadence, saves through the client, and returns focus to the chain", async () => {
    const client = fakeClient();
    const update = vi.spyOn(client, "updateLink");
    render(<Harness client={client} />);
    const chain = rail().getByRole("button", { name: "Links for Deploy" });
    fireEvent.click(chain);
    fireEvent.click(badge("Deploy", "leads", "Rail"));
    fireEvent.click(within(dialog()).getByRole("radio", { name: /Follow/ }));
    expect(badge("Deploy", "leads", "Rail").getAttribute("aria-label")).toMatch(/Follow, unsaved/);
    expect(saveButton().textContent).toBe("Save 1 change");
    await act(async () => {
      fireEvent.click(saveButton());
    });
    expect(update).toHaveBeenCalledWith("l-a-b", {
      granting_session_id: "a",
      subject_session_id: "b",
      grants: LEAD,
      cadence: "per_turn",
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(chain));
  });

  it("peels Esc: the edge editor first, then asks before discarding, listing the changes", () => {
    const client = fakeClient();
    const spies = [vi.spyOn(client, "createLink"), vi.spyOn(client, "updateLink"), vi.spyOn(client, "deleteLink")];
    render(<Harness client={client} />);
    openOn("Deploy");
    fireEvent.click(rail().getByText("Solo"));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(within(dialog()).queryByRole("group", { name: /^Edit link/ })).toBeNull();
    fireEvent.keyDown(document, { key: "Escape" });
    const confirm = screen.getByRole("alertdialog", { name: "Discard 1 unsaved change?" });
    expect(within(confirm).getByText("Link: Deploy leads Solo")).toBeTruthy();
    fireEvent.click(within(confirm).getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    // Cancel asks too, and Discard drops everything without a write.
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Discard" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    for (const spy of spies) expect(spy).not.toHaveBeenCalled();
  });

  it("toggles a brief overview from the (i) button, closing any edge editor", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    const info = within(dialog()).getByRole("button", { name: "How links work" });
    fireEvent.click(badge("Deploy", "leads", "Rail"));
    expect(within(dialog()).getByRole("group", { name: /^Edit link/ })).toBeTruthy();
    fireEvent.click(info);
    expect(info.getAttribute("aria-expanded")).toBe("true");
    expect(within(dialog()).getByText(/reads and directs/)).toBeTruthy();
    expect(within(dialog()).queryByRole("group", { name: /^Edit link/ })).toBeNull();
    // Opening an edge editor closes the overview.
    fireEvent.click(badge("Deploy", "leads", "Rail"));
    expect(within(dialog()).queryByText(/reads and directs/)).toBeNull();
    fireEvent.click(info);
    fireEvent.click(info);
    expect(within(dialog()).queryByText(/reads and directs/)).toBeNull();
    expect(info.getAttribute("aria-expanded")).toBe("false");
    expect(within(dialog()).queryByText(/unsaved/)).toBeNull();
  });

  it("closes the overview on Esc before anything else, without asking to discard", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    fireEvent.click(rail().getByText("Solo"));
    fireEvent.click(within(dialog()).getByRole("button", { name: "How links work" }));
    expect(within(dialog()).getByText(/reads and directs/)).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(within(dialog()).queryByText(/reads and directs/)).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.getByRole("dialog")).toBeTruthy();
    // The next Esc reaches the discard flow as before.
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("alertdialog", { name: "Discard 1 unsaved change?" })).toBeTruthy();
  });

  it("draws no lane labels and no legend under the sky", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    for (const text of [/Leads \/ monitors this/, /Led \/ monitored by this/, /flows down/, /flows up/, /fast = Follow/]) {
      expect(within(dialog()).queryByText(text)).toBeNull();
    }
  });

  it("closes at once with nothing unsaved, from Esc, × or a click on the pane", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Deploy");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    openOn("Deploy");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    openOn("Deploy");
    fireEvent.click(within(dialog()).getByRole("button", { name: /^Center on Rail/ }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    fireEvent.click(screen.getByTestId("scrim"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("reports a transient partial failure, retries from fresh server state, and finishes", async () => {
    const client = fakeClient();
    const del = vi.spyOn(client, "deleteLink");
    const create = vi.spyOn(client, "createLink").mockRejectedValueOnce(new Error("router request failed: 502"));
    render(<Harness client={client} />);
    openOn("Deploy");
    fireEvent.click(badge("Deploy", "leads", "Rail"));
    fireEvent.click(within(dialog()).getByRole("button", { name: /Unlink/ }));
    fireEvent.click(rail().getByText("Solo"));
    expect(saveButton().textContent).toBe("Save 2 changes");
    await act(async () => {
      fireEvent.click(saveButton());
    });
    expect(within(dialog()).getByRole("alert").textContent).toBe(
      "Saved 1 of 2. Couldn't save Deploy → Solo: router request failed: 502",
    );
    expect(saveButton().textContent).toBe("Retry");
    // The unlink landed: discarding now would leave it, so the confirm says so.
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    expect(
      screen.getByRole("alertdialog", { name: "Some changes are already saved. Discard 1 unsaved change?" }),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    await act(async () => {
      fireEvent.click(saveButton());
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    // The delete that landed was not sent again.
    expect(del).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledTimes(2);
    expect((await client.getSessionLinks("x")).map((l) => l.granting_session_id)).toEqual(["a"]);
  });

  it("marks a refused write's line red with the server's sentence, and keeps Save", async () => {
    const client = fakeClient();
    vi.spyOn(client, "createLink").mockRejectedValue(
      new RouterValidationError([
        { field: "grants", message: "orchestrate is refused: the subject session has a live pipeline run" },
      ]),
    );
    render(<Harness client={client} />);
    openOn("Deploy");
    fireEvent.click(rail().getByText("Solo"));
    await act(async () => {
      fireEvent.click(saveButton());
    });
    expect(badge("Deploy", "leads", "Solo").hasAttribute("data-bad")).toBe(true);
    const ed = within(dialog()).getByRole("group", { name: "Edit link Deploy → Solo" });
    expect(within(ed).getByText(/Couldn't save: Orchestrate is refused/)).toBeTruthy();
    expect(within(dialog()).getByRole("alert").textContent).toMatch(/Change that link, then save again\.$/);
    expect(saveButton().textContent).toBe("Save 1 change");
    // Changing the line is the fix: as a monitor it no longer needs orchestrate.
    fireEvent.click(within(ed).getByRole("button", { name: "◉ Monitors" }));
    expect(badge("Deploy", "monitors", "Solo").hasAttribute("data-bad")).toBe(true);
  });

  it("blocks Save on a loop of leaders and names the loop", () => {
    render(<Harness client={fakeClient()} />);
    openOn("Merge");
    fireEvent.click(rail().getByText("Captain"));
    expect(within(dialog()).getByRole("alert").textContent).toBe(
      "Can't save: Merge ♛→ Captain ♛→ Deploy ♛→ Merge is a loop of leaders. Change one link to Monitors or unlink it.",
    );
    expect((saveButton() as HTMLButtonElement).disabled).toBe(true);
    expect(badge("Merge", "leads", "Captain").hasAttribute("data-bad")).toBe(true);
    expect(within(dialog()).getByText(/This closes a loop of leaders/)).toBeTruthy();
    fireEvent.click(within(dialog()).getByRole("button", { name: "◉ Monitors" }));
    expect((saveButton() as HTMLButtonElement).disabled).toBe(false);
  });

  it("refuses a session in its own worktree of the same workspace: dimmed, and a click adds nothing", () => {
    const sessions = [
      session("a", "Deploy", { workspace: "mads", worktree_dir: "/wt/a" }),
      session("y", "Isolated", { workspace: "mads", worktree_dir: "/wt/y" }),
      session("z", "Elsewhere", { workspace: "other", worktree_dir: "/wt/z" }),
    ];
    render(<Harness client={fakeClient([])} sessions={sessions} links={[]} />);
    openOn("Deploy");
    const card = rail().getByText("Isolated").closest("[data-rail-card]")!;
    expect(card.getAttribute("data-link-edit")).toBe("blocked");
    fireEvent.click(card);
    expect(within(dialog()).queryByRole("group")).toBeNull();
    expect(card.className).toMatch(/nope/);
    // Links may cross workspaces.
    fireEvent.click(rail().getByText("Elsewhere"));
    expect(within(dialog()).getByRole("group", { name: "Edit link Deploy → Elsewhere" })).toBeTruthy();
  });
});

describe("LinkModal wrap layout", () => {
  beforeEach(() => {
    FLAGS.linkedSessions = true;
  });
  afterEach(() => {
    FLAGS.linkedSessions = false;
  });

  // A hub held by 3 (one leader, two monitors) that holds 9, in mixed roles and cadences.
  const SUBS = Array.from({ length: 9 }, (_, i) => session(`s${i + 1}`, `Sub ${i + 1}`));
  const HUB_SESSIONS = [session("h", "Hub"), session("u1", "Up 1"), session("u2", "Up 2"), session("u3", "Up 3"), ...SUBS];
  const CADS: LinkCadence[] = ["on_signal", "per_turn", "manual_only"];
  const HUB_LINKS = [
    link("u1", "h", "leader"),
    link("u2", "h", "monitor", "per_turn"),
    link("u3", "h", "monitor", "manual_only"),
    ...SUBS.map((s, i) => link("h", s.id, i % 3 === 1 ? "monitor" : "leader", CADS[i % 3])),
  ];

  function openHub() {
    render(<Harness client={fakeClient(HUB_LINKS)} sessions={HUB_SESSIONS} links={HUB_LINKS} />);
    openOn("Hub");
    const d = dialog();
    const at = (id: string) => {
      const el = d.querySelector(`[data-node="${id}"]`) as HTMLElement;
      return { x: parseFloat(el.style.left), y: parseFloat(el.style.top) };
    };
    return { d, at };
  }

  it("lays 9 subjects out in rows of 4, 4 and 1, each centred, stacking outward", () => {
    const { d, at } = openHub();
    const ys = SUBS.map((s) => at(s.id).y);
    const rows = [...new Set(ys)].sort((a, b) => a - b);
    expect(rows).toHaveLength(3);
    expect(rows.map((y) => ys.filter((v) => v === y).length)).toEqual([4, 4, 1]);
    // In order: row 0 nearest the hub, the 9th alone in the outermost row, centred.
    expect(at("s1").y).toBe(rows[0]);
    expect(at("s5").y).toBe(rows[1]);
    expect(at("s9")).toEqual({ x: SKY_W / 2, y: rows[2] });
    expect(rows[0]).toBeGreaterThan(at("h").y);
    for (const y of rows) {
      const xs = SUBS.filter((s) => at(s.id).y === y).map((s) => at(s.id).x);
      expect(xs.reduce((a, b) => a + b, 0) / xs.length).toBeCloseTo(SKY_W / 2);
    }
    // The sky grows taller, not wider.
    const sky = d.querySelector("[data-sky]") as HTMLElement;
    expect(parseFloat(sky.style.width)).toBe(SKY_W);
    expect(parseFloat(sky.style.height)).toBeGreaterThan(400);
  });

  it("keeps a side of 4 or fewer on one row, as before", () => {
    const { at } = openHub();
    expect(new Set(["u1", "u2", "u3"].map((id) => at(id).y)).size).toBe(1);
    expect(at("u2").x).toBe(SKY_W / 2);
    const one = layoutSky(["a", "b", "c", "d"], ["e"]);
    expect(one.height).toBe(400);
    expect(new Set(one.placed.map((p) => p.row))).toEqual(new Set([0]));
    // A row-0 line is today's single curve, holder first.
    expect(one.placed.every((p) => p.wire.length === 2)).toBe(true);
    expect(one.placed[0].wire[1]).toEqual({ x: one.focus.x, y: one.focus.y - 28 });
  });

  it("routes a line to a far row through a gutter of row 0, never across a nearer node", () => {
    const ids = Array.from({ length: 13 }, (_, i) => `n${i}`);
    for (const lay of [layoutSky([], ids), layoutSky(ids, [])]) {
      const row0 = lay.placed.filter((p) => p.row === 0);
      expect(row0).toHaveLength(PER);
      const y0 = row0[0].y;
      const far = lay.placed.filter((p) => p.row > 0);
      for (const p of far) {
        // The straight run through the gutter spans row 0's band, at one x.
        expect(p.wire).toHaveLength(4);
        const [a, b] = [p.wire[1], p.wire[2]];
        expect(a.x).toBe(b.x);
        expect(Math.min(a.y, b.y)).toBeLessThan(y0 - NODE_HALF);
        expect(Math.max(a.y, b.y)).toBeGreaterThan(y0 + NODE_HALF);
        for (const n of row0) expect(Math.abs(a.x - n.x)).toBeGreaterThan(NODE_W / 2);
      }
      // Lines sharing a gutter are told apart.
      const xs = far.map((p) => p.wire[1].x);
      expect(new Set(xs).size).toBe(xs.length);
    }
    // Upward lines still run holder → subject: they end at the centred session.
    const up = layoutSky(ids, []);
    const last = up.placed[up.placed.length - 1].wire;
    expect(last[last.length - 1]).toEqual({ x: up.focus.x, y: up.focus.y - 28 });
    expect(up.focus.y - up.placed[0].y).toBe(GAP_F);
  });

  it("draws the far row's line through the gutter in the rendered path, too", () => {
    const { d, at } = openHub();
    const key = badge("Hub", "leads", "Sub 9").getAttribute("data-edge")!;
    const path = d.querySelector(`[data-wire="${key}"]`)!.getAttribute("d")!;
    // M focus, C into the gutter, L straight down it, C into the node.
    const parts = path.split(/ (?=[MCL] )/);
    expect(parts.map((q) => q[0])).toEqual(["M", "C", "L", "C"]);
    const x = parseFloat(parts[2].split(" ")[1]);
    for (const id of ["s1", "s2", "s3", "s4"]) expect(Math.abs(x - at(id).x)).toBeGreaterThan(NODE_W / 2);
  });

  it("renders no sideways scroll container", () => {
    const { d } = openHub();
    expect(d.querySelector('[class*="skyScroll"]')).toBeNull();
    const body = d.querySelector("[data-link-body]") as HTMLElement;
    expect(body.className).toMatch(/mb/);
    expect(body.contains(d.querySelector("[data-sky]"))).toBe(true);
  });

  it("opens the edge editor for a row-2 edge, beside its badge", () => {
    const { d } = openHub();
    const b = badge("Hub", "leads", "Sub 9");
    fireEvent.click(b);
    const ed = within(d).getByRole("group", { name: "Edit link Hub → Sub 9" });
    // Inside the sky, so it scrolls with the body and shares the badge's coordinates.
    expect(d.querySelector("[data-sky]")!.contains(ed)).toBe(true);
    const bx = parseFloat(b.style.left);
    const by = parseFloat(b.style.top);
    expect(parseFloat(ed.style.left)).toBe(bx + 20);
    expect(parseFloat(ed.dataset.top!)).toBe(by - 150);
    expect(within(ed).getByRole("radio", { name: /Muted/ }).getAttribute("aria-checked")).toBe("true");
  });
});
