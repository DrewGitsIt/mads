import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { Locator, Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import {
  RESUMED_LINES,
  STOP_PICKUP_FILE,
  STOP_PICKUP_LINES,
  STOP_PICKUP_TASK,
} from "./lib/pipelineScenario";
import { createSession, createWorkspace, importTasks, pollEventOfType, setupWorkspace } from "./lib/seed";

// Stop a run mid agent stage, then pick the task up in a new run
// (docs/PIPELINE-LEASES-DESIGN.md Part B). The resumable task's executor
// (lib/pipelineScenario.ts) announces a session ref, writes a file into its
// worktree and streams slowly; Stop leaves it Interrupted with its work kept,
// Plan says where it resumes, and the next run resumes that same agent
// session (the fake's `resumed: true` rule) and finishes it.

const SHOTS = "/tmp/mads-stop-pickup-shots";
const QUICK_TASK = "sp journey: quick follow-on";
const PERSON_TASK = "sp journey: someone's own work";

interface Envelope {
  seq: number;
  type: string;
  task_id?: string;
  payload?: Record<string, unknown>;
}

async function shot(page: Page, name: string) {
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: true });
}

async function events(routerURL: string, sessionId: string, type: string): Promise<Envelope[]> {
  const res = await fetch(`${routerURL}/events?session_id=${sessionId}&types=${type}`);
  if (!res.ok) throw new Error(`events: status ${res.status}`);
  return (await res.json()) as Envelope[];
}

async function taskStatus(routerURL: string, id: string): Promise<string> {
  const res = await fetch(`${routerURL}/tasks/${id}`);
  if (!res.ok) throw new Error(`getTask: status ${res.status}`);
  return ((await res.json()) as { status: string }).status;
}

/** Live pipeline leases (any run's), straight from the Data Service. */
async function pipelineLeases(dataURL: string): Promise<{ task_id: string; holder: string }[]> {
  const res = await fetch(`${dataURL}/leases?kind=pipeline`, { headers: { "X-Mads-Actor": "pipeline" } });
  if (!res.ok) throw new Error(`leases: status ${res.status}`);
  return (await res.json()) as { task_id: string; holder: string }[];
}

/** A person moves a task to in_progress themselves (the Data Service's default actor is "user"). */
async function setInProgressAsUser(dataURL: string, id: string): Promise<void> {
  const get = await fetch(`${dataURL}/tasks/${id}`);
  if (!get.ok) throw new Error(`getTask: status ${get.status}`);
  const task = await get.json();
  const put = await fetch(`${dataURL}/tasks/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Mads-Actor": "user" },
    body: JSON.stringify({ ...task, status: "in_progress" }),
  });
  if (!put.ok) throw new Error(`setInProgress: status ${put.status}: ${await put.text()}`);
}

test.describe("tasks tab: stop a run and pick its work up", () => {
  test.describe.configure({ mode: "serial" });

  let sessionId: string;
  let sessionName: string;
  let workspaceDir: string;
  let taskId: string;
  let quickId: string;
  let personId: string;

  test.beforeAll(async () => {
    mkdirSync(SHOTS, { recursive: true });
    const routerURL = process.env.MADS_ROUTER_URL!;
    // Its own repo: the backup branch Stop leaves is checked here.
    workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-stop-pickup-"));
    setupWorkspace(workspaceDir);
    const stamp = Date.now();
    const workspace = await createWorkspace(routerURL, {
      name: `e2e stop-pickup ${stamp}`,
      repos: [{ path: workspaceDir, primary: true }],
    });
    sessionName = `e2e stop-pickup journey ${stamp}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake", workspace: workspace.id });
    sessionId = session.id;
    [{ id: taskId }] = await importTasks(routerURL, sessionId, [STOP_PICKUP_TASK]);
  });

  async function openTasksTab(page: Page, routerURL: string) {
    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();
    return page.locator("#workspace-panel-tasks");
  }

  const runTrigger = (panel: Locator) => panel.locator('button[aria-haspopup="menu"]').first();
  const group = (panel: Locator, label: string) => panel.getByRole("region", { name: new RegExp(`^${label}`) });
  const row = (panel: Locator, id: string) => panel.locator(`li[data-task="${id}"]`);
  const git = (...args: string[]) => execFileSync("git", args, { cwd: workspaceDir, stdio: "pipe" }).toString().trim();

  test("stop mid agent stage, then a new run resumes the same agent session", async ({ page, routerURL }) => {
    test.setTimeout(150_000);
    const dataURL = process.env.MADS_DATA_URL!;
    const panel = await openTasksTab(page, routerURL);
    const metro = panel.getByTestId("metro");
    const lens = panel.getByTestId("task-lens");
    const terminal = lens.getByTestId("terminal-view");

    // ── a. Run until the executor is mid-turn with its session ref ───────
    await expect(runTrigger(panel)).toHaveText(/New run/);
    await panel.getByRole("button", { name: "Run 1 task" }).click();
    await expect(runTrigger(panel)).toContainText("Run 1");
    await expect(runTrigger(panel)).toContainText("running");
    await expect(metro.locator('[data-stack="stage:executor"]').locator(`button[data-task="${taskId}"]`)).toBeVisible({
      timeout: 15_000,
    });
    await expect
      .poll(async () => (await events(routerURL, sessionId, "stage.attempt_ref")).filter((e) => e.task_id === taskId).length, {
        timeout: 10_000,
      })
      .toBe(1);
    const [refEvent] = (await events(routerURL, sessionId, "stage.attempt_ref")).filter((e) => e.task_id === taskId);
    expect(refEvent.payload).toMatchObject({ stage: "executor", attempt: 1 });
    const ref = refEvent.payload!.ref as string;
    expect(ref).toBeTruthy();

    await metro.locator(`button[data-task="${taskId}"]`).click();
    const stages = lens.getByRole("list", { name: "Stages" });
    await expect(stages.getByRole("button", { name: "architect: done" })).toBeVisible();
    await expect(stages.getByRole("button", { name: "executor: running" })).toBeVisible();
    // The session has a ref, so the lens opens on its turns; the raw log has the stream.
    await lens.getByRole("radio", { name: "Raw log" }).click();
    await expect(terminal).toContainText(STOP_PICKUP_LINES[1], { timeout: 10_000 });
    await shot(page, "a-running");

    // ── b. Stop ──────────────────────────────────────────────────────────
    await page.keyboard.press("Escape");
    await expect(lens).toBeHidden();
    await panel.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(runTrigger(panel)).toContainText("stopped", { timeout: 30_000 });
    await expect(group(panel, "Interrupted").locator(`li[data-task="${taskId}"]`)).toHaveAttribute(
      "data-status",
      "interrupted",
    );
    await expect(panel.getByRole("button", { name: "New run", exact: true })).toBeVisible();
    // The task's own status is untouched: still in progress, and nobody holds it.
    expect(await taskStatus(routerURL, taskId)).toBe("in_progress");
    await expect
      .poll(async () => (await pipelineLeases(dataURL)).filter((l) => l.task_id === taskId), { timeout: 10_000 })
      .toEqual([]);
    // The stray file the executor left was committed, and the branch backed up.
    expect(git("log", "-1", "--format=%s", `backup/${taskId}`)).toBe("auto-commit: interrupted work");
    expect(git("show", "--name-only", "--format=", `backup/${taskId}`)).toContain(STOP_PICKUP_FILE);
    await shot(page, "b-stopped");

    // Another task to run next time, and one a person set in progress
    // themselves: that one is theirs, not Interrupted.
    [{ id: quickId }, { id: personId }] = await importTasks(routerURL, sessionId, [QUICK_TASK, PERSON_TASK]);
    await setInProgressAsUser(dataURL, personId);
    expect(await taskStatus(routerURL, personId)).toBe("in_progress");

    // ── c. Plan ──────────────────────────────────────────────────────────
    await panel.getByRole("button", { name: "New run", exact: true }).click();
    await expect(runTrigger(panel)).toHaveText(/New run/);
    const willRun = group(panel, "Will run");
    await expect(willRun.locator(`li[data-task="${taskId}"]`)).toContainText("Interrupted · resumes at executor");
    await expect(panel.getByRole("checkbox", { name: `Include ${STOP_PICKUP_TASK}` })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(willRun.locator(`li[data-task="${quickId}"]`)).toBeVisible();
    await expect(group(panel, "Not in this run").locator(`li[data-task="${personId}"]`)).toBeVisible();
    // The Interrupted task and the ready one; not the person's.
    const runButton = panel.getByRole("button", { name: /^Run \d+ tasks?$/ });
    await expect(runButton).toHaveText(/Run 2 tasks/);
    // The token waits where the run will resume it.
    await expect(metro.locator('[data-stack="stage:executor"]').locator(`button[data-task="${taskId}"]`)).toBeVisible();
    await shot(page, "c-plan");

    // ── d. Interrupted, so its details are editable ──────────────────────
    await row(panel, taskId).getByRole("button", { name: `Edit ${STOP_PICKUP_TASK}` }).click();
    const sheet = page.getByRole("complementary").filter({ has: page.getByLabel("Title") });
    await expect(sheet.getByLabel("Title")).toHaveValue(STOP_PICKUP_TASK);
    await expect(sheet.getByLabel("Title")).not.toHaveAttribute("readonly", /.*/);
    await expect(sheet.getByRole("note").filter({ hasText: "Locked while the pipeline works on it" })).toHaveCount(0);
    await expect(sheet.getByRole("button", { name: "Save" })).toHaveCount(1);
    await shot(page, "d-sheet-editable");
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();

    // ── e. The new run picks it up ───────────────────────────────────────
    await runButton.click();
    await expect(runTrigger(panel)).toContainText("Run 2");
    await expect(runTrigger(panel)).toContainText("running");
    await expect(group(panel, "Running").locator(`li[data-task="${taskId}"]`)).toBeVisible({ timeout: 15_000 });
    // f. The person's in-progress task is not this run's.
    await expect(group(panel, "Not in this run").locator(`li[data-task="${personId}"]`)).toBeVisible();

    await metro.locator(`button[data-task="${taskId}"]`).click();
    await expect(lens.getByTestId("pickup-marker")).toHaveText(/Picked up from Run 1 · resumes at executor/);
    await expect(stages.getByRole("button", { name: "architect: done in Run 1" })).toBeVisible();
    // The timeline's attempt note, and the output header's.
    await expect(lens.getByTestId("attempt-note")).toHaveText(/resumed from Run 1/);
    await expect(lens.getByText("resumed from Run 1", { exact: true })).toBeVisible();
    await lens.getByRole("radio", { name: "Raw log" }).click();
    await expect(terminal).toContainText(RESUMED_LINES[0], { timeout: 15_000 });
    await shot(page, "e-picked-up");

    // The same agent session, resumed: the same attempt, its ref.
    const picked = (await events(routerURL, sessionId, "stage.picked_up")).filter((e) => e.task_id === taskId);
    expect(picked).toHaveLength(1);
    expect(picked[0].payload).toMatchObject({ mode: "resume", from_stage: "executor", resumed_from_run_seq: expect.any(Number) });
    const resumedStart = (await events(routerURL, sessionId, "stage.started")).filter(
      (e) => e.task_id === taskId && e.payload?.stage === "executor" && e.payload?.resumed === true,
    );
    expect(resumedStart).toHaveLength(1);
    expect(resumedStart[0].payload).toMatchObject({ attempt: 1 });
    const refs = (await events(routerURL, sessionId, "stage.attempt_ref")).filter((e) => e.task_id === taskId);
    expect(refs.map((e) => e.payload!.ref)).toEqual(expect.arrayContaining([ref]));
    expect(new Set(refs.map((e) => e.payload!.ref))).toEqual(new Set([ref]));

    await page.keyboard.press("Escape");
    await expect(lens).toBeHidden();
    await expect(runTrigger(panel)).toContainText("completed", { timeout: 60_000 });
    await expect(group(panel, "Done").locator(`li[data-task="${taskId}"]`)).toBeVisible();
    await expect(group(panel, "Done").locator(`li[data-task="${quickId}"]`)).toBeVisible();
    await expect(group(panel, "Not in this run").locator(`li[data-task="${personId}"]`)).toBeVisible();
    expect(await taskStatus(routerURL, taskId)).toBe("done");
    // The run never touched the person's task.
    expect(await taskStatus(routerURL, personId)).toBe("in_progress");
    expect((await events(routerURL, sessionId, "stage.started")).filter((e) => e.task_id === personId)).toEqual([]);
    // The work merged into the session's branch, including what Run 1 left behind.
    const session = (await (await fetch(`${routerURL}/sessions/${sessionId}`)).json()) as {
      worktree_branch?: string;
      session?: { worktree_branch?: string };
    };
    const sessionBranch = session.worktree_branch ?? session.session?.worktree_branch;
    expect(sessionBranch).toBeTruthy();
    expect(git("show", `${sessionBranch}:${STOP_PICKUP_FILE}`)).toBe("half done");
    await shot(page, "e-done");
  });

  test("the finished run still shows the pickup", async ({ page, routerURL }) => {
    await pollEventOfType(routerURL, sessionId, "pipeline.completed", 5_000);
    const panel = await openTasksTab(page, routerURL);
    await expect(runTrigger(panel)).toContainText("Run 2");
    await expect(runTrigger(panel)).toContainText("completed");
    await panel.getByTestId("metro").locator(`button[data-task="${taskId}"]`).click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens.getByTestId("pickup-marker")).toHaveText(/Picked up from Run 1 · resumes at executor/);
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "architect: done in Run 1" })).toBeVisible();
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "merge: done" })).toBeVisible();
    await shot(page, "f-past-run");
  });
});
