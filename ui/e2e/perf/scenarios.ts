import type { Locator, Page } from "@playwright/test";
import type { PerfStack } from "./stack";

/** What perfseed wrote (services/data/cmd/perfseed Manifest), as far as the scenarios need it. */
export interface Manifest {
  profile: string;
  sessions: { id: string; name: string }[];
  parent: {
    id: string;
    name: string;
    delegates: number;
    running: { harness_id: string; description: string }[];
    with_file?: { harness_id: string; description: string };
    no_file?: { harness_id: string; description: string };
  };
  switch_a: { id: string; name: string };
  switch_b: { id: string; name: string };
  total_events: number;
  total_bytes: number;
  counts: Record<string, number>;
  bytes: Record<string, number>;
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
  /** Resolves once the view shows data (not just its frame). */
  ready(ctx: ScenarioCtx): Promise<void>;
  /** Let the page go quiet before the next leg, so each leg's bytes are its own. */
  quiesceAfter?: boolean;
}

export interface Scenario {
  id: string;
  title: string;
  /** Gets the page to the scenario's starting view and lets it settle. Not measured. */
  prepare(ctx: ScenarioCtx): Promise<void>;
  legs: Leg[];
}

// --- Selectors. Only what main already renders: the baseline is main as-is,
// so a scenario can't lean on a test hook main doesn't have.

const T = 120_000;
const rail = (p: Page) => p.locator('[aria-label="Session list"]');
const railCard = (p: Page, id: string) => rail(p).locator(`[data-rail-card][data-rail-id="${id}"]`);
/** The open session's header: its rename button carries its name, from GET /sessions/{id}. */
const header = (p: Page, id: string) =>
  p.getByTitle("Rename session").filter({ hasText: new RegExp(`^${escapeRe(nameOf(id))}$`) });
const nameOf = (id: string) => m().sessions.find((s) => s.id === id)?.name ?? id;
const nav = (p: Page) => p.getByRole("navigation", { name: "Main navigation" });

async function until(what: string, f: () => Promise<boolean>, timeoutMs = T): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!(await f().catch(() => false))) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

/** Every seeded session's card is in the rail, each with its status line drawn. */
async function railReady(ctx: ScenarioCtx): Promise<void> {
  const n = ctx.manifest.sessions.length;
  await until(`${n} rail cards with status lines`, async () => {
    const cards = rail(ctx.page).locator("[data-rail-card]");
    return (await cards.count()) >= n && (await rail(ctx.page).locator("[data-rail-card] [role=status]").count()) >= n;
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

async function openSession(ctx: ScenarioCtx, id: string): Promise<void> {
  await railCard(ctx.page, id).click();
  await sessionReady(ctx, id);
}

const openLeg = (name: string, id: () => string, quiesceAfter = false): Leg => ({
  name,
  act: (ctx) => railCard(ctx.page, id()).click(),
  ready: (ctx) => sessionReady(ctx, id()),
  quiesceAfter,
});

let current: Manifest | undefined;
const m = () => current!;

function delegateScenario(id: string, title: string, pick: (man: Manifest) => { description: string } | undefined): Scenario {
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
      await openSession(ctx, m().parent.id);
      await row(ctx).waitFor({ state: "visible", timeout: 60_000 });
    },
    legs: [{ name: "open delegate", act: (ctx) => row(ctx).click(), ready: delegateReady }],
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
        ready: railReady,
      },
    ],
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
  },
  // The live path: the delegate has its own transcript file.
  delegateScenario("3-open-delegate", "Open a delegate from its parent (transcript file)", (man) => man.parent.with_file ?? man.parent.running[0]),
  // The fallback: no file, so the view reads the session's whole subagent log back.
  delegateScenario("3b-open-delegate-nofile", "Open a delegate with no transcript file (log fallback)", (man) => man.parent.no_file),
  {
    id: "4-retro-roundtrip",
    title: "Retro → Sessions round trip",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [
      {
        name: "→Retro",
        act: (ctx) => nav(ctx.page).getByRole("button", { name: "Retro", exact: true }).click(),
        ready: retroReady,
        quiesceAfter: true,
      },
      {
        name: "→Sessions",
        act: (ctx) => nav(ctx.page).getByRole("button", { name: "Sessions", exact: true }).click(),
        ready: railReady,
        quiesceAfter: true,
      },
    ],
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
        ready: async (ctx) => {
          await ctx.page.getByLabel("Describe your goal").waitFor({ state: "visible", timeout: T });
          await railReady(ctx);
        },
      },
    ],
  },
  // Phase 5 slots in here: 6 (pinned long-lived streams) and 7 (network
  // drop and resume, DelayProxy.cutAll), plus correctness assertions run
  // after 1, 4 and 7 (Scenario could grow an `assert(ctx)` hook).
];

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
