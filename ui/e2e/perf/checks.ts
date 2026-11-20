import type { Page } from "@playwright/test";
import { data, liveFixture, router, startStuckFastModel, waitFor } from "./fixtures";
import { bootRig, type CheckResult, type Plan, type Result } from "./measure";
import type { Build } from "./stack";
import type { Manifest } from "./scenarios";

/**
 * Correctness checks (docs/PERF-HARNESS-PLAN.md §5): a faster design must
 * not win by dropping state. Each is a user-visible assertion on what the
 * page shows, whatever carries the state there (replay, a REST snapshot,
 * a live stream), and each passes on main. One fresh stack per check per
 * build, delays off.
 */

const T = 90_000;
const rail = (p: Page) => p.locator('[aria-label="Session list"]');
const card = (p: Page, id: string) => rail(p).locator(`[data-rail-card][data-rail-id="${id}"]`);
const nav = (p: Page) => p.getByRole("navigation", { name: "Main navigation" });

async function step(name: string, f: () => Promise<string | void>, knownOnMain?: string): Promise<CheckResult> {
  try {
    const detail = await f();
    return { name, ok: true, detail: detail || undefined, knownOnMain };
  } catch (err) {
    return { name, ok: false, detail: (err instanceof Error ? err.message : String(err)).split("\n")[0].slice(0, 200), knownOnMain };
  }
}

async function load(page: Page, url: string): Promise<void> {
  await page.goto(url);
  await rail(page).locator("[data-rail-card]").first().waitFor({ state: "visible", timeout: T });
}

async function retroRoundTrip(page: Page): Promise<void> {
  await nav(page).getByRole("button", { name: "Retro", exact: true }).click();
  await page.getByRole("button", { name: "Generate retro" }).waitFor({ state: "visible", timeout: T });
  await nav(page).getByRole("button", { name: "Sessions", exact: true }).click();
  await rail(page).locator("[data-rail-card]").first().waitFor({ state: "visible", timeout: T });
}

interface Check {
  name: string;
  title: string;
  run(c: { page: Page; rig: Awaited<ReturnType<typeof bootRig>>; manifest: Manifest }): Promise<CheckResult[]>;
  needsFastModel?: boolean;
}

export const CHECKS: Check[] = [
  {
    name: "a-permission",
    title: "a pending permission request shows on the rail after a reload and after Retro → Sessions",
    // The request is a log record (session.permission_requested with no
    // resolution) written to the Data Service before the page loads — the
    // shape a parked harness leaves; the fake provider has no permission flow.
    run: async ({ page, rig, manifest }) => {
      const id = manifest.switch_b.id;
      await data(rig.stack, "POST", "/events", {
        type: "session.permission_requested",
        session_id: id,
        actor: "perf",
        payload: { request_id: "perf-permission-1", tool: "Bash", tool_use_id: "toolu_perf_permission" },
      });
      const shows = async () => {
        try {
          await card(page, id).getByRole("status").filter({ hasText: /Needs approval/ }).waitFor({ state: "visible", timeout: 30_000 });
        } catch {
          const said = (await card(page, id).getByRole("status").first().innerText().catch(() => "?")).replace(/\s+/g, " ");
          throw new Error(`the card's status reads "${said}"`);
        }
      };
      return [
        await step("after a cold load", async () => {
          await load(page, rig.stack.appURL);
          await shows();
        }),
        await step("after a reload", async () => {
          await page.reload();
          await shows();
        }),
        await step(
          "after Retro → Sessions",
          async () => {
            await retroRoundTrip(page);
            await shows();
          },
          "main's bus keeps its per-type cursors while Retro's own subscriptions hold it open, so the remounted rail's replay of " +
            "session.permission_requested is deduped as already delivered and never reaches the new fold",
        ),
      ];
    },
  },
  {
    name: "b-queue",
    title: "a session's queued messages match the server after a reload",
    // A live fake session whose turn streams until interrupted ("slow"), with
    // two more messages sent behind it: the Runner queues them.
    run: async ({ page, rig }) => {
      const fx = await liveFixture(rig.stack, { slow: true });
      await waitFor(async () => {
        const st = await router<{ agent_state?: { state?: string } }>(rig.stack, "GET", `/sessions/${fx.session.id}`);
        return st.agent_state?.state === "working";
      }, 30_000, "the slow turn to start");
      // queue: true, as the Composer sends mid-turn: the Runner holds them.
      for (const t of ["echo perf queued one", "echo perf queued two"]) {
        await router(rig.stack, "POST", `/sessions/${fx.session.id}/messages`, { text: t, queue: true });
      }
      const server = async () =>
        ((await router<{ queued?: { text: string }[] } | { text: string }[]>(rig.stack, "GET", `/sessions/${fx.session.id}/queue`)) as unknown) as
          | { queued?: { text: string }[] }
          | { text: string }[];
      const serverTexts = async () => {
        const q = await server();
        return (Array.isArray(q) ? q : (q.queued ?? [])).map((x) => x.text);
      };
      const uiTexts = async () => {
        const list = page.getByTestId("queued-messages");
        await list.waitFor({ state: "visible", timeout: T });
        // One row per queued message (QueuedMessages.tsx): badge, text, remove button.
        return (await list.locator(":scope > div").allInnerTexts()).map((s) => s.trim());
      };
      const same = async (label: string) => {
        const want = await serverTexts();
        if (want.length === 0) throw new Error("the server has no queue (fixture failed)");
        await waitFor(async () => {
          const got = await uiTexts();
          return want.every((t) => got.some((g) => g.includes(t))) && got.length === want.length;
        }, 30_000, `${label}: the UI's queue to match the server's ${JSON.stringify(want)}`);
        return `${want.length} queued`;
      };
      return [
        await step("after opening the session", async () => {
          await load(page, rig.stack.appURL);
          await card(page, fx.session.id).click();
          return same("open");
        }),
        await step("after a reload", async () => {
          await page.reload();
          await card(page, fx.session.id).click();
          return same("reload");
        }),
      ];
    },
  },
  {
    name: "c-retro-progress",
    title: "an in-progress retro shows its progress after starting it, and after leaving and coming back",
    // A Fast Model that never answers keeps the retro in progress.
    needsFastModel: true,
    run: async ({ page, rig, manifest }) => {
      const progress = () => page.getByRole("status", { name: "Retro generation progress" }).waitFor({ state: "visible", timeout: T });
      return [
        await step("after starting it", async () => {
          await load(page, rig.stack.appURL);
          await nav(page).getByRole("button", { name: "Retro", exact: true }).click();
          await page.getByRole("checkbox", { name: manifest.switch_a.name }).check();
          await page.getByRole("button", { name: "Generate retro" }).click();
          await progress();
        }),
        await step(
          "after Sessions → Retro",
          async () => {
            await nav(page).getByRole("button", { name: "Sessions", exact: true }).click();
            await rail(page).locator("[data-rail-card]").first().waitFor({ state: "visible", timeout: T });
            await nav(page).getByRole("button", { name: "Retro", exact: true }).click();
            await page.getByRole("status", { name: "Retro generation progress" }).waitFor({ state: "visible", timeout: 30_000 });
          },
          "useRetroGeneration keeps the job in component state: a remounted Retro surface starts at the picker, whatever the bus delivers",
        ),
      ];
    },
  },
  {
    name: "d-pipeline-running",
    title: "a session with a running pipeline shows it running, after a reload",
    run: async ({ page, rig }) => {
      const fx = await liveFixture(rig.stack, { pipeline: true });
      const running = async () => {
        await card(page, fx.session.id).click();
        await page.getByTestId("tasks-run-badge").waitFor({ state: "visible", timeout: T });
        return `badge ${(await page.getByTestId("tasks-run-badge").innerText()).trim()}`;
      };
      return [
        await step("after opening the session", async () => {
          await load(page, rig.stack.appURL);
          return running();
        }),
        await step("after a reload", async () => {
          await page.reload();
          await rail(page).locator("[data-rail-card]").first().waitFor({ state: "visible", timeout: T });
          return running();
        }),
      ];
    },
  },
  {
    name: "e-provisioning",
    title: "a session being provisioned renders its steps",
    // Started from the launchpad, as a user does; the step labels are caught
    // in the page (probe text marker) however briefly they show.
    run: async ({ page, rig }) => {
      return [
        await step("from the launchpad", async () => {
          await load(page, rig.stack.appURL);
          await page.evaluate(() =>
            (window as unknown as { __perf: { arm(n: string, s: unknown): void } }).__perf.arm("provisioning", {
              kind: "text",
              any: ["Using current directory", "Setting up worktree", "Installing commit hook", "Connecting capabilities", "Starting agent"],
            }),
          );
          await page.getByLabel("Describe your goal").fill("echo perf provisioning check");
          await page.getByRole("button", { name: "Start session" }).click();
          await waitFor(
            async () => !!(await page.evaluate(() => (window as unknown as { __perf: { markers: Record<string, number> } }).__perf.markers.provisioning)),
            30_000,
            "a provisioning step label",
          );
        }),
      ];
    },
  },
];

export function checkByName(name: string): Check {
  const c = CHECKS.find((x) => x.name === name || x.name.startsWith(`${name}-`));
  if (!c) throw new Error(`unknown check ${name}; have ${CHECKS.map((x) => x.name).join(", ")}`);
  return c;
}

export async function runCheck(opts: { plan: Plan; build: Build; name: string; manifest: Manifest }): Promise<Result> {
  const check = checkByName(opts.name);
  const result: Result = {
    scenario: `check:${check.name}`,
    build: opts.build.label,
    mode: "off",
    rep: 1,
    browser: opts.plan.browser,
    ok: false,
    legs: [],
    det: {},
    timing: {},
    eventsConnects: [],
    requestsByRoute: {},
    queuedByRoute: {},
    windowCapped: false,
    socketOwners: [],
    browserForeign: [],
    checks: [],
    requests: [],
  };
  const fm = check.needsFastModel ? await startStuckFastModel() : undefined;
  let rig: Awaited<ReturnType<typeof bootRig>> | undefined;
  try {
    rig = await bootRig({
      plan: opts.plan,
      build: opts.build,
      mode: "off",
      tag: `check-${check.name}-${opts.build.label}`,
      manifest: opts.manifest,
      fastModelURL: fm?.url,
    });
    result.checks = await check.run({ page: rig.page, rig, manifest: opts.manifest });
    // A step known to fail on main doesn't fail the check; anything else does.
    result.ok = result.checks.length > 0 && result.checks.every((c) => c.ok || c.knownOnMain);
    if (!result.ok) result.error = result.checks.filter((c) => !c.ok && !c.knownOnMain).map((c) => `${c.name}: ${c.detail}`).join("; ");
  } catch (err) {
    result.error = err instanceof Error ? err.message : String(err);
  } finally {
    await rig?.teardown();
    await fm?.close();
  }
  return result;
}
