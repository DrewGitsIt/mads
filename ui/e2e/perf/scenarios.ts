import type { Locator, Page } from "@playwright/test";
import type { MarkerSpec } from "./probe";
import type { PerfStack } from "./stack";
import type { CheckResult } from "./measure";
import { liveFixture, renameSession, type LiveFixture } from "./fixtures";

/** What perfseed wrote (services/data/cmd/perfseed Manifest), as far as the scenarios need it. */
export interface Manifest {
  profile: string;
  sessions: { id: string; name: string }[];
  parent: ParentRef;
  /** A typical parent (~1,000 subagent events; scenario 3t). */
  typical?: ParentRef;
  workspace_id?: string;
  /** Which rail cards mean "loaded": every session (seeded), or named ones (realism, where Done is capped). */
  rail?: { n?: number; ids?: string[] };
  switch_a: { id: string; name: string };
  switch_b: { id: string; name: string };
  total_events: number;
  total_bytes: number;
  counts: Record<string, number>;
  bytes: Record<string, number>;
}

export interface ParentRef {
  id: string;
  name: string;
  delegates: number;
  subagent_events?: number;
  running: { harness_id: string; description: string }[];
  with_file?: { harness_id: string; description: string };
  no_file?: { harness_id: string; description: string };
}

export interface ScenarioCtx {
  page: Page;
  stack: PerfStack;
  manifest: Manifest;
  browser: "chromium" | "electron";
  /** Waits until the page is quiet: no /events bytes, no new or open short requests (delayed ones aside). */
  quiesce(): Promise<void>;
}

/** One click (or navigation) and the data marker that says its view has rendered its data. */
export interface Leg {
  name: string;
  act(ctx: ScenarioCtx): Promise<void>;
  /** The data marker, checked inside the page (probe.ts) from the click on. */
  marker(ctx: ScenarioCtx, clickAt: number): MarkerSpec;
  /** Let the page go quiet before the next leg, so each leg's bytes are its own. */
  quiesceAfter?: boolean;
  /** The act loads a new document: the marker is armed in it by an init script. */
  navigates?: boolean;
}

export interface Scenario {
  id: string;
  title: string;
  /** Gets the page to the scenario's starting view and lets it settle. Not measured. */
  prepare(ctx: ScenarioCtx): Promise<void>;
  legs: Leg[];
  /** The live-event smoke run after the measured window (seeded and realism alike). */
  smoke?(ctx: ScenarioCtx): Promise<CheckResult>;
}

// --- Selectors. Only what main already renders: the baseline is main as-is,
// so a scenario can't lean on a test hook main doesn't have.

const T = Number(process.env.PERF_PREPARE_WAIT_MS ?? 30_000);
/** Scenario 7b's outage (PERF_OFFLINE_S, default 20). */
const OFFLINE_S = Number(process.env.PERF_OFFLINE_S ?? 20);
const rail = (p: Page) => p.locator('[aria-label="Session list"]');
const railCard = (p: Page, id: string) => rail(p).locator(`[data-rail-card][data-rail-id="${id}"]`);
/** The open session's header: its rename button carries its name, from GET /sessions/{id}. */
const header = (p: Page, id: string, name = nameOf(id)) =>
  p.getByTitle("Rename session").filter({ hasText: new RegExp(`^${escapeRe(name)}$`) });
const nameOf = (id: string) => m().sessions.find((s) => s.id === id)?.name ?? id;
const nav = (p: Page) => p.getByRole("navigation", { name: "Main navigation" });

async function until(what: string, f: () => Promise<boolean>, timeoutMs = T): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!(await f().catch(() => false))) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

/** Which rail cards mean the rail has loaded its data. */
const railSpec = (man: Manifest): { n?: number; ids?: string[] } => man.rail ?? { n: man.sessions.length };
const railMarker = (ctx: ScenarioCtx): MarkerSpec => ({ kind: "rail", ...railSpec(ctx.manifest) });

/** Every expected rail card is in, each with its status line drawn. */
async function railReady(ctx: ScenarioCtx): Promise<void> {
  const spec = railSpec(ctx.manifest);
  await until("the rail's cards with status lines", async () => {
    if (spec.ids) {
      for (const id of spec.ids) if ((await railCard(ctx.page, id).locator("[role=status]").count()) === 0) return false;
      return true;
    }
    const n = spec.n ?? 1;
    return (await rail(ctx.page).locator("[data-rail-card]").count()) >= n && (await rail(ctx.page).locator("[data-rail-card] [role=status]").count()) >= n;
  });
}

/** The session's header named from its record, and its conversation pane past "loading". */
async function sessionReady(ctx: ScenarioCtx, id: string): Promise<void> {
  await header(ctx.page, id).waitFor({ state: "visible", timeout: T });
  // The seeded sessions have no transcript: the pane's settled state is its empty/missing notice or its stream.
  await ctx.page
    .locator('#workspace-panel-conversation [data-testid="transcript-missing"], #workspace-panel-conversation [data-testid="conversation-stream"]')
    .first()
    .waitFor({ state: "visible", timeout: T });
}

/** The delegate's own view with at least one tool row of its stream rendered. */
async function delegateReady(ctx: ScenarioCtx): Promise<void> {
  await ctx.page.locator('[data-testid="delegate-stream"] [data-testid="activity-row"]').first().waitFor({ state: "visible", timeout: T });
}

/** The retro picker listing the seeded sessions (GET /sessions answered). */
async function retroReady(ctx: ScenarioCtx): Promise<void> {
  await ctx.page.getByRole("button", { name: "Generate retro" }).waitFor({ state: "visible", timeout: T });
  await ctx.page.getByText(ctx.manifest.switch_a.name, { exact: true }).first().waitFor({ state: "visible", timeout: T });
}

async function load(ctx: ScenarioCtx): Promise<void> {
  if (ctx.browser === "electron") await ctx.page.reload();
  else await ctx.page.goto(ctx.stack.appURL);
  await railReady(ctx);
}

async function openSession(ctx: ScenarioCtx, id: string, name = nameOf(id)): Promise<void> {
  await railCard(ctx.page, id).click();
  await header(ctx.page, id, name).waitFor({ state: "visible", timeout: T });
}

const openLeg = (name: string, id: () => string, quiesceAfter = false): Leg => ({
  name,
  act: (ctx) => railCard(ctx.page, id()).click(),
  marker: () => ({ kind: "session", name: nameOf(id()) }),
  quiesceAfter,
});

const retroLeg: Leg = {
  name: "→Retro",
  act: (ctx) => nav(ctx.page).getByRole("button", { name: "Retro", exact: true }).click(),
  marker: () => ({ kind: "retro", name: m().switch_a.name }),
  quiesceAfter: true,
};
const sessionsLeg: Leg = {
  name: "→Sessions",
  act: (ctx) => nav(ctx.page).getByRole("button", { name: "Sessions", exact: true }).click(),
  marker: railMarker,
  quiesceAfter: true,
};

/**
 * The live-event smoke: a session renamed through the Router API shows its
 * new name on its rail card within 2 s (timed inside the page).
 */
async function renameSmoke(ctx: ScenarioCtx): Promise<CheckResult> {
  const id = m().switch_b.id;
  const name = `perf smoke ${Date.now() % 1_000_000}`;
  const at = Date.now();
  await ctx.page.evaluate(
    ([sid, n]) =>
      (window as unknown as { __perf: { arm(n: string, s: unknown): void } }).__perf.arm("smoke", { kind: "cardText", id: sid, text: n }),
    [id, name] as const,
  );
  await renameSession(ctx.stack, id, name);
  const deadline = Date.now() + 10_000;
  for (;;) {
    const seen = await ctx.page.evaluate(() => (window as unknown as { __perf: { markers: Record<string, number> } }).__perf.markers.smoke);
    if (seen) {
      const ms = Math.round(seen - at);
      return { name: "live event: rename shows on the rail within 2 s", ok: ms <= 2000, detail: `${ms} ms` };
    }
    if (Date.now() > deadline) return { name: "live event: rename shows on the rail within 2 s", ok: false, detail: "not within 10 s" };
    await new Promise((r) => setTimeout(r, 50));
  }
}

let current: Manifest | undefined;
const m = () => current!;

function delegateScenario(
  id: string,
  title: string,
  pick: (man: Manifest) => { description: string } | undefined,
  parentOf: (man: Manifest) => ParentRef | undefined = (man) => man.parent,
): Scenario {
  const row = (ctx: ScenarioCtx): Locator => {
    const d = pick(m());
    if (!d) throw new Error(`${id}: the workload has no such delegate (seed with -transcripts)`);
    return ctx.page.getByTestId("delegate-rows").getByRole("button", { name: new RegExp(escapeRe(d.description)) }).first();
  };
  return {
    id,
    title,
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      const p = parentOf(m());
      if (!p) throw new Error(`${id}: the workload has no such parent`);
      await openSession(ctx, p.id, p.name);
      await row(ctx).waitFor({ state: "visible", timeout: 30_000 });
    },
    legs: [{ name: "open delegate", act: (ctx) => row(ctx).click(), marker: () => ({ kind: "delegate" }) }],
    smoke: renameSmoke,
  };
}

export const SCENARIOS: Scenario[] = [
  {
    id: "1-cold-load",
    title: "Cold window load",
    prepare: async () => {},
    legs: [
      {
        name: "load",
        act: async (ctx) => {
          if (ctx.browser === "electron") await ctx.page.reload({ waitUntil: "commit" });
          else await ctx.page.goto(ctx.stack.appURL, { waitUntil: "commit" });
        },
        marker: railMarker,
        navigates: true,
      },
    ],
    smoke: renameSmoke,
  },
  {
    id: "2-switch-session",
    title: "Switch session A → B → A (quiet between legs)",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [openLeg("A→B", () => m().switch_b.id, true), openLeg("B→A", () => m().switch_a.id, true)],
    smoke: renameSmoke,
  },
  // The live path: the delegate has its own transcript file.
  // The heaviest parent, modelled on the live top session (~15k subagent events at L).
  delegateScenario("3-open-delegate", "Open a delegate from the heaviest parent (transcript file)", (man) => man.parent.with_file ?? man.parent.running[0]),
  // The fallback: no file, so the view reads the session's whole subagent log back.
  delegateScenario("3b-open-delegate-nofile", "Open a delegate of the heaviest parent with no transcript file (log fallback)", (man) => man.parent.no_file),
  // A typical parent: ~1,000 subagent events (the live delegate Drew opened in ~4 s).
  delegateScenario(
    "3t-open-delegate-typical",
    "Open a delegate from a typical parent (~1,000 subagent events; transcript file)",
    (man) => man.typical?.with_file ?? man.typical?.running[0],
    (man) => man.typical,
  ),
  {
    id: "4-retro-roundtrip",
    title: "Retro → Sessions round trip",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [retroLeg, sessionsLeg],
    smoke: renameSmoke,
  },
  {
    id: "5-launchpad",
    title: "Session → launchpad (control)",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [
      {
        name: "→launchpad",
        act: (ctx) => ctx.page.getByRole("button", { name: "New session" }).click(),
        marker: (ctx) => ({ kind: "launchpad", rail: railSpec(ctx.manifest) }),
      },
    ],
    smoke: renameSmoke,
  },
  pinnedScenario(),
  {
    id: "7b-offline",
    title: "Offline for a while, then back (connections refused: backoff, health, probes)",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [
      {
        name: `offline ${OFFLINE_S} s → resumed`,
        act: (ctx) => ctx.stack.proxy.offline(OFFLINE_S * 1000),
        // Resumed: a new /events stream has opened after the network came back.
        marker: (_ctx, clickAt) => ({ kind: "esOpenAfter", after: clickAt + OFFLINE_S * 1000 }),
      },
    ],
    smoke: renameSmoke,
  },
  {
    id: "7-drop-resume",
    title: "Network drop and resume (every socket cut, as on sleep/wake)",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [
      {
        name: "cut → resumed",
        act: (ctx) => ctx.stack.proxy.cutAll(),
        // Resumed: the bus has opened a new /events stream (then the rule waits for it to catch up).
        marker: (_ctx, clickAt) => ({ kind: "esAfter", after: clickAt }),
      },
    ],
    smoke: renameSmoke,
  },
];

/**
 * Scenario 6: the switch and Retro round trip of 2 and 4, starting from a
 * session holding long-lived streams the way a working one does — a live
 * fake-provider session with a background process streaming into the open
 * tool dock, and its Tasks tab mounted on a live pipeline attempt
 * (fixtures.ts). Session A, B and the Retro surface are the seeded ones.
 */
function pinnedScenario(): Scenario {
  let fx: LiveFixture | undefined;
  const sid = () => fx!.session.id;
  return {
    id: "6-pinned-streams",
    title: "Switch and Retro round trip with long-lived streams pinned (dock background output + live pipeline attempt)",
    prepare: async (ctx) => {
      fx = await liveFixture(ctx.stack, { background: true, pipeline: true, delegate: true });
      await load(ctx);
      await ctx.quiesce().catch(() => {}); // the live attempt keeps the page busy; prepare only
      await railCard(ctx.page, sid()).click();
      await header(ctx.page, sid(), fx.session.name).waitFor({ state: "visible", timeout: T });
      // The Tasks tab first (it stays mounted once visited), then the conversation with the dock open.
      await ctx.page.getByRole("tab", { name: /Tasks/ }).click();
      await ctx.page.getByTestId("tasks-run-badge").waitFor({ state: "visible", timeout: 60_000 });
      await ctx.page.getByRole("tab", { name: /Conversation|Chat/ }).first().click();
      const conv = ctx.page.locator("#workspace-panel-conversation");
      const row = conv.getByTestId("activity-row").filter({ hasText: fx.backgroundPrompt });
      const group = conv.getByTestId("activity-group").last();
      await group.waitFor({ state: "visible", timeout: 30_000 });
      if (!(await row.isVisible())) await group.getByRole("button").first().click();
      await row.locator("[data-dock-source]").click();
      // The dock is streaming the process's output (a line a second).
      await ctx.page.locator("[data-activity-dock] .xterm-rows").getByText(/line \d+/).first().waitFor({ state: "visible", timeout: 30_000 });
    },
    legs: [
      // Inside S, its streams still open: the Changes tab (the changeset), then one of S's delegates.
      {
        name: "S: Changes tab",
        act: (ctx) => ctx.page.getByRole("tab", { name: /Changes/ }).click(),
        marker: () => ({ kind: "exists", sel: '#workspace-panel-files [data-testid="changes-list"], #workspace-panel-files [data-testid="changes-totals"], #workspace-panel-files [data-testid="changes-work-state"]' }),
        quiesceAfter: true,
      },
      {
        name: "S: Conversation tab",
        act: (ctx) => ctx.page.getByRole("tab", { name: /Conversation/ }).click(),
        marker: () => ({ kind: "exists", sel: '#workspace-panel-conversation [data-testid="conversation-stream"]' }),
        quiesceAfter: true,
      },
      {
        name: "S: open delegate",
        act: (ctx) => ctx.page.getByTestId("delegate-rows").getByRole("button", { name: /perf pinned delegate/ }).first().click(),
        marker: () => ({ kind: "delegate" }),
        quiesceAfter: true,
      },
      { name: "S→A", act: (ctx) => railCard(ctx.page, m().switch_a.id).click(), marker: () => ({ kind: "session", name: m().switch_a.name }), quiesceAfter: true },
      { name: "A→S", act: (ctx) => railCard(ctx.page, sid()).click(), marker: () => ({ kind: "session", name: fx!.session.name }), quiesceAfter: true },
      retroLeg,
      sessionsLeg,
    ],
    smoke: renameSmoke,
  };
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function useManifest(man: Manifest): void {
  current = man;
}

export function scenarioById(id: string): Scenario {
  const s = SCENARIOS.find((x) => x.id === id) ?? SCENARIOS.find((x) => x.id.startsWith(`${id}-`));
  if (!s) throw new Error(`unknown scenario ${id}; have ${SCENARIOS.map((x) => x.id).join(", ")}`);
  return s;
}
