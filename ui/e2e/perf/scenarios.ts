import type { Locator, Page } from "@playwright/test";
import type { PerfStack } from "./stack";

/** What perfseed wrote (services/data/cmd/perfseed Manifest), as far as the scenarios need it. */
export interface Manifest {
  profile: string;
  sessions: { id: string; name: string }[];
  parent: { id: string; name: string; delegates: number; running: { harness_id: string; description: string }[] };
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
  /** Waits until the page is quiet: no stream bytes and no new short requests for a while. */
  quiesce(): Promise<void>;
}

/** One click (or navigation) and the DOM marker that says its view is there. */
export interface Leg {
  name: string;
  act(ctx: ScenarioCtx): Promise<void>;
  marker(ctx: ScenarioCtx): Locator;
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

const rail = (p: Page) => p.locator('[aria-label="Session list"]');
const railCard = (p: Page, id: string) => rail(p).locator(`[data-rail-card][data-rail-id="${id}"]`);
/** The open session's header: its rename button carries its name. */
const workspace = (p: Page, id: string) =>
  p.getByTitle("Rename session").filter({ hasText: new RegExp(`^${escapeRe(nameOf(id))}$`) });
const nameOf = (id: string) => m().sessions.find((s) => s.id === id)?.name ?? id;
const nav = (p: Page) => p.getByRole("navigation", { name: "Main navigation" });

/** The app's first view: the rail with its seeded sessions. */
const railLoaded = (ctx: ScenarioCtx) => rail(ctx.page).locator("[data-rail-card]").first();

async function load(ctx: ScenarioCtx): Promise<void> {
  if (ctx.browser === "electron") await ctx.page.reload();
  else await ctx.page.goto(ctx.stack.appURL);
  await railLoaded(ctx).waitFor({ state: "visible", timeout: 120_000 });
}

async function openSession(ctx: ScenarioCtx, id: string): Promise<void> {
  await railCard(ctx.page, id).click();
  await workspace(ctx.page, id).waitFor({ state: "visible", timeout: 120_000 });
}

const openLeg = (name: string, id: () => string): Leg => ({
  name,
  act: (ctx) => railCard(ctx.page, id()).click(),
  marker: (ctx) => workspace(ctx.page, id()),
});

let current: Manifest | undefined;
const m = () => current!;

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
        marker: railLoaded,
      },
    ],
  },
  {
    id: "2-switch-session",
    title: "Switch session A → B → A",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().switch_a.id);
    },
    legs: [openLeg("A→B", () => m().switch_b.id), openLeg("B→A", () => m().switch_a.id)],
  },
  {
    id: "3-open-delegate",
    title: "Open a delegate from its parent",
    prepare: async (ctx) => {
      await load(ctx);
      await ctx.quiesce();
      await openSession(ctx, m().parent.id);
      await delegateRow(ctx).waitFor({ state: "visible", timeout: 60_000 });
    },
    legs: [
      {
        name: "open delegate",
        act: (ctx) => delegateRow(ctx).click(),
        marker: (ctx) => ctx.page.getByTestId("delegate-view"),
      },
    ],
  },
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
        marker: (ctx) => ctx.page.getByRole("button", { name: "Generate retro" }),
      },
      {
        name: "→Sessions",
        act: (ctx) => nav(ctx.page).getByRole("button", { name: "Sessions", exact: true }).click(),
        marker: railLoaded,
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
        marker: (ctx) => ctx.page.getByLabel("Describe your goal"),
      },
    ],
  },
  // Phase 5 slots in here: 6 (pinned long-lived streams) and 7 (network
  // drop and resume, DelayProxy.cutAll), plus correctness assertions run
  // after 1, 4 and 7 (Scenario could grow an `assert(ctx)` hook).
];

/** The parent's first running-at-seed delegate, concluded at boot: inside the rail's fade window. */
function delegateRow(ctx: ScenarioCtx): Locator {
  const d = m().parent.running[0];
  return ctx.page.getByTestId("delegate-rows").getByRole("button", { name: new RegExp(escapeRe(d.description)) }).first();
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function useManifest(man: Manifest): void {
  current = man;
}

export function scenarioById(id: string): Scenario {
  const s = SCENARIOS.find((x) => x.id === id || x.id.startsWith(`${id}-`));
  if (!s) throw new Error(`unknown scenario ${id}; have ${SCENARIOS.map((x) => x.id).join(", ")}`);
  return s;
}
