import type { Page } from "@playwright/test";
import { data, liveFixture, router, startStuckFastModel, waitFor } from "./fixtures";
import { bootRig, type CheckResult, type Plan, type Result } from "./measure";
import type { Build } from "./stack";
import type { GitFixture, GitSession, Manifest } from "./scenarios";
import { appendFileSync } from "node:fs";
import { fxGit } from "./stack";
import { resolveCheck } from "./checkNames";

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
  /** Extra environment for the stack (the fake's start delay). */
  env?: Record<string, string>;
  run(c: { page: Page; rig: Awaited<ReturnType<typeof bootRig>>; manifest: Manifest }): Promise<CheckResult[]>;
  needsFastModel?: boolean;
}

export const CHECKS: Check[] = [
  {
    name: "a-permission",
    title: "a pending permission request shows on the rail after a load, a reload and Retro → Sessions",
    // A live fake session parked on its permission hook (the fake's
    // "permission" dialect): the Runner holds the request, so the log and
    // GET /sessions/{id}/permission-requests agree, as in production.
    run: async ({ page, rig }) => {
      const fx = await liveFixture(rig.stack, { permission: true });
      const id = fx.session.id;
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
    // UI-only: the generation view is local state fed by the retro job's
    // own events; no list a reload rebuilds from the bus shows an
    // in-progress retro on main. Reported, excluded from bus comparisons.
    title: "(UI-only) an in-progress retro shows its progress after starting it, and after leaving and coming back",
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
      const sid = fx.session.id;
      // The server's count: in-progress tasks the run claimed (moved to
      // in_progress with actor "pipeline"), what the badge counts.
      const serverCount = async () => {
        const tasks = await router<{ id: string; status: string }[]>(rig.stack, "GET", `/tasks?session_id=${sid}&status=in_progress`);
        const claims = await router<{ task_id?: string; actor?: string; payload?: { to?: string } }[]>(
          rig.stack,
          "GET",
          `/events?session_id=${sid}&types=task.status_changed`,
        );
        const claimed = new Set(claims.filter((e) => e.actor === "pipeline" && e.payload?.to === "in_progress").map((e) => e.task_id));
        return tasks.filter((t) => claimed.has(t.id)).length;
      };
      const running = async () => {
        await card(page, sid).click();
        const badge = page.getByTestId("tasks-run-badge");
        await badge.waitFor({ state: "visible", timeout: T });
        // The running flag can land (a REST seed) before the task count:
        // the badge is a bare spinner until the count is known.
        const want = await serverCount();
        if (want === 0) throw new Error("the server has no task claimed by the pipeline (fixture failed)");
        const deadline = Date.now() + 2_000;
        let got = "";
        for (;;) {
          got = (await badge.innerText().catch(() => "")).trim();
          if (/^\d+$/.test(got) || Date.now() >= deadline) break;
          await page.waitForTimeout(50);
        }
        if (!/^\d+$/.test(got)) throw new Error(`the badge showed no task count within 2 s (text "${got}"; server: ${want} running)`);
        if (Number(got) !== want) throw new Error(`the badge counts ${got} running, the server ${want}`);
        return `badge ${got}`;
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
    title: "a session being provisioned renders its steps, and still does after a reload part-way",
    // The fake harness takes 10 s to start (MADS_FAKE_START_DELAY_MS), so
    // the session sits in "Starting agent" long enough to reload mid-way.
    env: { MADS_FAKE_START_DELAY_MS: process.env.PERF_PROVISION_DELAY_MS ?? "10000" },
    run: async ({ page, rig }) => {
      // The strip (role=status "Session provisioning") with its step spans
      // (five; four when the hook step is skipped). Checked on every DOM
      // mutation: POST /sessions answers only once the runner's Start is
      // done (the session child logs "listening on" after it), so on main
      // the strip mounts after the last step and its replayed
      // provider_ready unmounts it some 20 ms later — between two frames,
      // which a once-a-frame check missed now and then.
      const armSteps = (name: string) =>
        page.evaluate(
          (n) =>
            (window as unknown as { __perf: { arm(n: string, s: unknown): void } }).__perf.arm(n, {
              kind: "exists",
              sel: '[aria-label="Session provisioning"] > span',
              n: 4,
              sync: true,
            }),
          name,
        );
      const seen = (name: string) =>
        waitFor(
          async () => !!(await page.evaluate((n) => (window as unknown as { __perf: { markers: Record<string, number> } }).__perf.markers[n], name)),
          30_000,
          "the provisioning strip's steps",
        );
      let sessionId = "";
      return [
        await step("from the launchpad", async () => {
          await load(page, rig.stack.appURL);
          await armSteps("steps");
          await page.getByLabel("Describe your goal").fill("echo perf provisioning check");
          await page.getByRole("button", { name: "Start session" }).click();
          await seen("steps");
          // The session it just created: the newest one.
          const all = await router<{ id: string; created_at: string }[]>(rig.stack, "GET", "/sessions");
          sessionId = [...all].sort((a, b) => b.created_at.localeCompare(a.created_at))[0]?.id ?? "";
        }),
        await step(
          "after a reload part-way",
          async () => {
            if (!sessionId) throw new Error("the new session wasn't found");
            await page.reload();
            await rail(page).locator("[data-rail-card]").first().waitFor({ state: "visible", timeout: T });
            await armSteps("steps-reload");
            await card(page, sessionId).click();
            await seen("steps-reload");
          },
          "SessionsSurface shows the provisioning strip only for the session this window just created (provisioningSessionId, " +
            "component state): after a reload the session opens without it, whatever the bus replays",
        ),
      ];
    },
  },
];

// --- Checks f–h: real git (perfseed gitfx).

/**
 * How long the UI may take to match a fresh server compute after a change
 * (PERF_CONVERGE_S, default 15). Main's own path: the change's events (or
 * the repo watcher: 1 s of quiet on the git dir, at most 5 s) → the UI's
 * 1.5 s refetch debounce → a recompute (the heavy session's changeset is
 * 4–5 s on main, and one may already be in flight when the change lands).
 * 15 s = the watcher's worst case + the debounce + two changeset computes;
 * main measured 3–9 s. A design that pushes fresh values should beat it.
 */
const CONVERGE_S = Number(process.env.PERF_CONVERGE_S ?? 15);
/** g: how soon after a change the "updating" indicator must show. */
const UPDATING_WITHIN_MS = Number(process.env.PERF_UPDATING_WITHIN_MS ?? 2000);
const files = (p: Page) => p.locator("#workspace-panel-files");

function gitFixture(manifest: Manifest): GitFixture {
  if (!manifest.git) throw new Error("the workload has no git fixture (GITFX=1)");
  return manifest.git;
}

async function say(rig: Awaited<ReturnType<typeof bootRig>>, id: string, text: string): Promise<void> {
  await router(rig.stack, "POST", `/sessions/${id}/messages`, { text, queue: true });
}

async function turnEnds(rig: Awaited<ReturnType<typeof bootRig>>, id: string): Promise<void> {
  // The turn has started (or is over) and the agent is idle again.
  await new Promise((r) => setTimeout(r, 300));
  await waitFor(async () => (await router<{ agent_state?: { state?: string } }>(rig.stack, "GET", `/sessions/${id}`)).agent_state?.state === "idle", 60_000, `${id}'s turn to end`);
}

/** Opens a session's Changes tab and waits for its change set. */
async function openChangesOf(page: Page, s: GitSession): Promise<void> {
  await card(page, s.id).click();
  await page.getByTitle("Rename session").filter({ hasText: s.name }).waitFor({ state: "visible", timeout: T });
  await page.getByRole("tab", { name: /Changes/ }).click();
  await files(page).getByTestId("changes-totals").filter({ hasText: /\d+ files?/ }).first().waitFor({ state: "visible", timeout: T });
}

/** What the Changes tab says: its file count, the chip's numbers, and (popover open) the Land step's commits. */
interface GitView {
  files?: number;
  uncommitted?: number;
  toPush?: number;
  toLand?: number;
}

async function readView(page: Page): Promise<GitView> {
  return page.evaluate(() => {
    const panel = document.querySelector("#workspace-panel-files");
    const num = (re: RegExp, text: string | null | undefined) => {
      const m = text ? re.exec(text) : null;
      return m ? Number(m[1]) : undefined;
    };
    const totals = panel?.querySelector('[data-testid="changes-totals"]')?.textContent;
    const state = panel?.querySelector('[data-testid="changes-work-state"]')?.textContent;
    const land = document.querySelector('[data-testid="work-step-land"]')?.textContent;
    return {
      files: num(/(\d+) files?/, totals),
      uncommitted: num(/(\d+) uncommitted/, state) ?? (state && /^\s*committed/.test(state) ? 0 : undefined),
      toPush: num(/(\d+) to push/, state),
      toLand: num(/(\d+) commits? on /, land) ?? num(/(\d+) commits? to land/, state),
    };
  });
}

/**
 * Polls the view every 250 ms for CONVERGE_S, then takes a fresh server
 * compute (after the UI's last read, so the oracle's own side effects — GET
 * /worktree may write the record's counts — can't help the UI) and compares
 * the last reading with it. Detail: when the view first matched for good.
 */
async function converges(page: Page, since: number, fields: (keyof GitView)[], oracle: () => Promise<GitView>): Promise<string> {
  const reads: [number, GitView][] = [];
  const deadline = since + CONVERGE_S * 1000;
  while (Date.now() < deadline) {
    reads.push([Date.now(), await readView(page)]);
    await page.waitForTimeout(250);
  }
  reads.push([Date.now(), await readView(page)]);
  const want = await oracle();
  const same = (v: GitView) => fields.every((f) => v[f] === want[f]);
  const last = reads[reads.length - 1][1];
  if (!same(last)) {
    const show = (v: GitView) => fields.map((f) => `${f} ${v[f] ?? "–"}`).join(", ");
    throw new Error(`after ${CONVERGE_S} s the UI shows ${show(last)}; a fresh compute says ${show(want)}`);
  }
  let at = reads.length - 1;
  while (at > 0 && same(reads[at - 1][1])) at--;
  return `matched ${((reads[at][0] - since) / 1000).toFixed(1)} s after the change (${fields.map((f) => `${f} ${want[f]}`).join(", ")})`;
}

/** Fresh server computes (side-effect free except GET /worktree's count write-back, read last). */
async function freshChanges(rig: Awaited<ReturnType<typeof bootRig>>, id: string): Promise<number> {
  return (await router<{ files: unknown[] }>(rig.stack, "GET", `/sessions/${id}/changeset?vs=start`)).files.length;
}
async function freshWorktree(rig: Awaited<ReturnType<typeof bootRig>>, id: string): Promise<{ uncommitted_count: number; unlanded_count: number }> {
  return router(rig.stack, "GET", `/sessions/${id}/worktree`);
}
async function freshAhead(rig: Awaited<ReturnType<typeof bootRig>>, id: string): Promise<number> {
  const repos = await router<{ primary?: boolean; worktree: { ahead: number } }[]>(rig.stack, "GET", `/sessions/${id}/repos`);
  return (repos.find((r) => r.primary) ?? repos[0])?.worktree.ahead ?? 0;
}

const rel = (s: GitSession, p: string) => p.slice(s.folder.length + 1);

/** A commit made outside mads (a terminal) in a fixture checkout. */
function commitOutside(s: GitSession, paths: string[], msg: string): void {
  for (const p of paths) appendFileSync(p, `// ${msg}\n`);
  fxGit(s.folder, ["add", "--", ...paths.map((p) => rel(s, p))]);
  fxGit(s.folder, ["commit", "-q", "--no-verify", "-m", msg]);
}

const GIT_CHECKS: Check[] = [
  {
    name: "f-git-converge",
    title: `real git: after an agent turn or a commit, the Changes list, header counts and branch facts match a fresh compute within ${CONVERGE_S} s`,
    run: async ({ page, rig, manifest }) => {
      const g = gitFixture(manifest);
      const A = g.sessions.isolated;
      const Ty = g.sessions.typical;
      const fresh = `${A.new_dir}/perf_converge_new.md`;
      return [
        await step("isolated heavy: an agent turn writes two files", async () => {
          await load(page, rig.stack.appURL);
          await openChangesOf(page, A);
          await say(rig, A.id, "echo perf converge warm-up");
          await turnEnds(rig, A.id);
          await page.waitForTimeout(3000);
          await say(rig, A.id, `write-file ${rel(A, A.edit_file)} perf converge edit`);
          await say(rig, A.id, `write-file ${rel(A, fresh)} perf converge new`);
          await turnEnds(rig, A.id);
          const since = Date.now();
          return converges(page, since, ["files", "uncommitted"], async () => ({
            files: await freshChanges(rig, A.id),
            uncommitted: (await freshWorktree(rig, A.id)).uncommitted_count,
          }));
        }),
        await step("isolated heavy: a commit outside mads (to land +1)", async () => {
          await files(page).getByRole("button", { name: "Where this session's work goes" }).click();
          await page.getByTestId("work-step-land").waitFor({ state: "visible", timeout: T });
          commitOutside(A, [A.edit_file, fresh], "perf converge: commit outside mads");
          const since = Date.now();
          return converges(page, since, ["files", "uncommitted", "toLand"], async () => {
            const wt = await freshWorktree(rig, A.id);
            return { files: await freshChanges(rig, A.id), uncommitted: wt.uncommitted_count, toLand: wt.unlanded_count };
          });
        }),
        await step("typical, in place: a commit outside mads (to push +1)", async () => {
          await page.keyboard.press("Escape");
          await openChangesOf(page, Ty);
          await page.waitForTimeout(2000);
          commitOutside(Ty, [Ty.edit_file], "perf converge: commit in place");
          const since = Date.now();
          return converges(page, since, ["files", "uncommitted", "toPush"], async () => ({
            files: await freshChanges(rig, Ty.id),
            uncommitted: (await freshWorktree(rig, Ty.id)).uncommitted_count,
            toPush: await freshAhead(rig, Ty.id),
          }));
        }),
      ];
    },
  },
  {
    name: "g-git-updating",
    title: "real git: a visible \"updating\" marker ([data-testid=git-updating]) while a recompute of what the session shows is running",
    run: async ({ page, rig, manifest }) => {
      const g = gitFixture(manifest);
      const A = g.sessions.isolated;
      const why =
        "main has no such marker: the Changes tab says \"Updating…\" only until its first answer, and nothing shows a refetch or a recount " +
        "(the contract design B is to meet; docs/PERF-HARNESS-PLAN.md, check g)";
      let changedAt = 0;
      return [
        await step(
          `shows within ${UPDATING_WITHIN_MS / 1000} s of a change`,
          async () => {
            await load(page, rig.stack.appURL);
            await openChangesOf(page, A);
            await say(rig, A.id, "echo perf updating warm-up");
            await turnEnds(rig, A.id);
            await page.waitForTimeout(3000);
            await page.evaluate(() =>
              (window as unknown as { __perf: { arm(n: string, s: unknown): void } }).__perf.arm("updating", { kind: "exists", sel: '[data-testid="git-updating"]', sync: true }),
            );
            changedAt = Date.now();
            await say(rig, A.id, `write-file ${rel(A, A.edit_file)} perf updating edit`);
            const deadline = changedAt + UPDATING_WITHIN_MS;
            for (;;) {
              const at = await page.evaluate(() => (window as unknown as { __perf: { markers: Record<string, number> } }).__perf.markers.updating);
              if (at) return `${Math.round(at - changedAt)} ms after the change`;
              if (Date.now() > deadline) throw new Error(`no [data-testid="git-updating"] within ${UPDATING_WITHIN_MS} ms`);
              await page.waitForTimeout(25);
            }
          },
          why,
        ),
        await step(
          `clears within ${CONVERGE_S} s, with the values a fresh compute gives`,
          async () => {
            await turnEnds(rig, A.id);
            const seen = await page.evaluate(() => (window as unknown as { __perf: { markers: Record<string, number> } }).__perf.markers.updating);
            if (!seen) throw new Error("the marker never showed");
            await page.locator('[data-testid="git-updating"]').waitFor({ state: "hidden", timeout: CONVERGE_S * 1000 });
            const v = await readView(page);
            const want = await freshChanges(rig, A.id);
            if (v.files !== want) throw new Error(`cleared showing ${v.files} files; a fresh compute says ${want}`);
            return `${v.files} files`;
          },
          why,
        ),
      ];
    },
  },
  {
    name: "h-git-dialogs",
    title: "real git: Commit, Land and Push opened right after a change show what a fresh compute says",
    run: async ({ page, rig, manifest }) => {
      const g = gitFixture(manifest);
      const A = g.sessions.isolated;
      const L = g.sessions.land;
      const dialog = () => page.locator('[aria-modal="true"]');
      const close = async () => {
        await dialog().getByRole("button", { name: "Cancel" }).click();
        await dialog().waitFor({ state: "hidden", timeout: T });
      };
      return [
        await step("Commit (isolated heavy, right after a turn writes a file)", async () => {
          await load(page, rig.stack.appURL);
          await openChangesOf(page, A);
          await say(rig, A.id, `write-file ${rel(A, A.new_dir + "/perf_dialog_new.md")} perf dialog new`);
          await turnEnds(rig, A.id);
          await files(page).getByTestId("changes-workchip").getByRole("button", { name: "Commit" }).click();
          const boxes = dialog().getByRole("checkbox", { name: /^Include / });
          await boxes.first().waitFor({ state: "visible", timeout: T });
          await page.waitForTimeout(500);
          const shown = (await boxes.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")!.slice("Include ".length)))).sort();
          const plan = await router<{ repos: { files: { path: string }[] }[] }>(rig.stack, "GET", `/sessions/${A.id}/commit-plan`);
          const want = plan.repos.flatMap((r) => r.files.map((f) => f.path)).sort();
          await close();
          if (JSON.stringify(shown) !== JSON.stringify(want)) {
            const missing = want.filter((p) => !shown.includes(p));
            const extra = shown.filter((p) => !want.includes(p));
            throw new Error(`the dialog lists ${shown.length} files, a fresh plan ${want.length} (missing ${missing.slice(0, 3).join(", ")}; extra ${extra.slice(0, 3).join(", ")})`);
          }
          if (!shown.some((p) => p.endsWith("perf_dialog_new.md"))) throw new Error("the new file is not in the dialog");
          return `${shown.length} files`;
        }),
        await step("Land (isolated to land, right after a commit outside mads)", async () => {
          await openChangesOf(page, L);
          await page.waitForTimeout(1000);
          commitOutside(L, [L.edit_file], "perf dialog: commit outside mads");
          // Land from the "where this session's work goes" popover (the chip's own Land is there only in its "to land" state).
          await files(page).getByRole("button", { name: "Where this session's work goes" }).click();
          await page.getByTestId("work-step-land").getByRole("button", { name: "Land" }).click();
          const n = dialog().getByText(/\d+ commits? to squash/);
          await n.waitFor({ state: "visible", timeout: T });
          const shown = Number(/(\d+)/.exec(await n.innerText())![1]);
          const want = (await router<{ to_land: number }>(rig.stack, "GET", `/sessions/${L.id}/land`)).to_land;
          await close();
          if (shown !== want) throw new Error(`the dialog shows ${shown} to squash, a fresh preview ${want}`);
          return `${shown} commits to squash`;
        }),
        await step("Push (isolated to land, right after that commit)", async () => {
          await files(page).getByRole("button", { name: "Where this session's work goes" }).click();
          await page.getByTestId("work-step-push").getByRole("button").first().click();
          const n = dialog().getByText(/^\d+ commits?:$/);
          await n.waitFor({ state: "visible", timeout: T });
          const shown = Number(/(\d+)/.exec(await n.innerText())![1]);
          const want = (await router<{ ahead: number }>(rig.stack, "GET", `/sessions/${L.id}/push?target=branch`)).ahead;
          await close();
          if (shown !== want) throw new Error(`the dialog shows ${shown} commits, a fresh preview ${want}`);
          return `${shown} commits`;
        }),
      ];
    },
  },
];
CHECKS.push(...GIT_CHECKS);

export function checkByName(name: string): Check {
  const full = resolveCheck(name);
  const c = CHECKS.find((x) => x.name === full);
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
      env: check.env,
    });
    result.checks = await check.run({ page: rig.page, rig, manifest: opts.manifest });
    // A step known to fail on main doesn't fail the check; anything else does.
    result.ok = result.checks.length > 0 && result.checks.every((c) => c.ok || c.knownOnMain);
    if (!result.ok) result.error = result.checks.filter((c) => !c.ok && !c.knownOnMain).map((c) => `${c.name}: ${c.detail}`).join("; ");
  } catch (err) {
    result.error = err instanceof Error ? err.message : String(err);
    // A check that throws before its steps run is a failure on record, not silence.
    if (result.checks.length === 0) result.checks = [{ name: "setup", ok: false, detail: result.error.split("\n")[0].slice(0, 200) }];
    result.ok = false;
  } finally {
    await rig?.teardown();
    await fm?.close();
  }
  return result;
}
