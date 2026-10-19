import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { Locator, Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import {
  ATTEMPT_ONE_LINES,
  JOURNEY_LENS_TASK,
  JOURNEY_ROOT_TASK,
  REVIEW_FAILURE,
} from "./lib/pipelineScenario";
import {
  createSession,
  createTask,
  createTemplate,
  createWorkspace,
  importTasks,
  linkDependency,
  pollEventOfType,
  setupWorkspace,
} from "./lib/seed";

// The pipeline runs inside the Tasks tab (docs/TASKS-PIPELINE-DESIGN.md):
// plan a run, start it from the run bar, watch it on the metro, zoom into a
// task, look back at an earlier attempt, use the graph's focus view, revisit
// the finished run, and cancel a run after a reload. Runs are launched by the
// UI itself (POST /pipelines → the real services/pipeline child), scripted by
// the suite-wide fake scenario in lib/pipelineScenario.ts: the lens task's
// executor streams slowly, and its code review fails once and loops back.

const SHOTS = "/tmp/mads-tasks-pipeline-shots";
const DEPENDENT_TASK = "tp journey: left-out dependent";
const DRAFT_TASK = "tp journey: draft";
// Depends on the lens task, so the lens task sits on the graph canvas rather
// than in the tray of tasks with no dependencies.
const FOLLOW_UP_TASK = "tp journey: follow-up";

async function shot(page: Page, name: string) {
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: true });
}

/** Occurrences of `needle` in the element's text (xterm splits lines across nodes). */
async function countText(locator: Locator, needle: string): Promise<number> {
  const text = (await locator.textContent()) ?? "";
  return text.split(needle).length - 1;
}

test.describe("tasks tab: pipeline journey", () => {
  test.describe.configure({ mode: "serial" });

  let sessionId: string;
  let sessionName: string;
  let templateName: string;
  let lensId: string;
  let rootId: string;
  let dependentId: string;
  let draftId: string;

  test.beforeAll(async () => {
    mkdirSync(SHOTS, { recursive: true });
    const routerURL = process.env.MADS_ROUTER_URL!;
    // A workspace of its own: the journey's runs merge into this repo, not
    // the suite's shared one.
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-tasks-pipeline-"));
    setupWorkspace(workspaceDir);
    const stamp = Date.now();
    const workspace = await createWorkspace(routerURL, {
      name: `e2e tasks-pipeline ${stamp}`,
      repos: [{ path: workspaceDir, primary: true }],
    });
    // A second template, so the picker has a choice to show.
    templateName = `tp-quick-fix-${stamp}`;
    await createTemplate(routerURL, {
      name: templateName,
      description: "Implement and verify. No design or review pass.",
      source: [
        `name: ${templateName}`,
        "stages:",
        "  - name: executor",
        "    agent: executor",
        "  - name: verify",
        "    command: make check",
        "",
      ].join("\n"),
    });

    sessionName = `e2e tasks-pipeline journey ${stamp}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake", workspace: workspace.id });
    sessionId = session.id;
    const [lens, root, dependent] = await importTasks(routerURL, sessionId, [
      JOURNEY_LENS_TASK,
      JOURNEY_ROOT_TASK,
      DEPENDENT_TASK,
    ]);
    lensId = lens.id;
    rootId = root.id;
    dependentId = dependent.id;
    await linkDependency(routerURL, dependentId, rootId);
    const [followUp] = await importTasks(routerURL, sessionId, [FOLLOW_UP_TASK]);
    await linkDependency(routerURL, followUp.id, lensId);
    draftId = (await createTask(routerURL, { session_id: sessionId, title: DRAFT_TASK, draft: true })).id;
  });

  async function openTasksTab(page: Page, routerURL: string) {
    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();
    return page.locator("#workspace-panel-tasks");
  }

  // The run menu's trigger: "New run" in Plan, "Run N <outcome>" otherwise.
  const runTrigger = (panel: Locator) => panel.locator('button[aria-haspopup="menu"]').first();
  const group = (panel: Locator, label: string) => panel.getByRole("region", { name: new RegExp(`^${label}`) });
  const row = (panel: Locator, id: string) => panel.locator(`li[data-task="${id}"]`);

  test("plan, run, zoom into a task, and look back at an earlier attempt", async ({ page, routerURL }) => {
    test.setTimeout(120_000);
    const panel = await openTasksTab(page, routerURL);

    // ── a. Plan ──────────────────────────────────────────────────────────
    await expect(runTrigger(panel)).toHaveText(/New run/);
    const runButton = panel.getByRole("button", { name: /^Run \d+ tasks?$/ });
    // Lens + its follow-up + root + the root's dependent; the draft can't run.
    await expect(runButton).toHaveText(/Run 4 tasks/);

    const templateTrigger = panel.getByRole("button", { name: /^Template: / });
    await expect(templateTrigger).toContainText("default-implementation");
    await templateTrigger.click();
    const templates = page.getByRole("menu", { name: "Templates" });
    await expect(templates.getByRole("menuitemradio", { name: /default-implementation/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(templates.getByRole("menuitemradio", { name: new RegExp(templateName) })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(templates).toBeHidden();

    // Drafts can't be ticked.
    const draftBox = panel.getByRole("checkbox", { name: `Include ${DRAFT_TASK}` });
    await expect(draftBox).toHaveAttribute("aria-disabled", "true");
    // aria-disabled: Playwright would wait forever for it to enable; click it anyway.
    await draftBox.click({ force: true });
    await expect(draftBox).toHaveAttribute("aria-checked", "false");
    await expect(runButton).toHaveText(/Run 4 tasks/);
    await shot(page, "plan");

    // Unticking a task another depends on: one warning line, and its
    // dependent drops out of the count with it.
    const rootBox = panel.getByRole("checkbox", { name: `Include ${JOURNEY_ROOT_TASK}` });
    await rootBox.click();
    await expect(rootBox).toHaveAttribute("aria-checked", "false");
    await expect(panel.getByText(`${DEPENDENT_TASK} won't run (depends on ${JOURNEY_ROOT_TASK})`)).toBeVisible();
    await expect(runButton).toHaveText(/Run 2 tasks/);
    await expect(row(panel, dependentId)).toContainText(`won't run: depends on ${JOURNEY_ROOT_TASK}, which is left out`);
    await shot(page, "plan-with-warning");

    // ── b. Run ───────────────────────────────────────────────────────────
    await runButton.click();
    await expect(runTrigger(panel)).toContainText("Run 1");
    await expect(runTrigger(panel)).toContainText("running");
    await expect(panel.getByRole("button", { name: "Cancel run" })).toBeEnabled();

    const metro = panel.getByTestId("metro");
    const lensToken = metro.locator(`button[data-task="${lensId}"]`);
    // The executor is the slow stage; the token rides the line to it.
    await expect(metro.locator('[data-stack="stage:executor"]').locator(`button[data-task="${lensId}"]`)).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("tasks-run-badge")).toHaveText("1");

    await expect(group(panel, "Running").locator(`li[data-task="${lensId}"]`)).toBeVisible();
    const notInRun = group(panel, "Not in this run");
    await expect(notInRun.locator(`li[data-task="${rootId}"]`)).toBeVisible();
    await expect(notInRun.locator(`li[data-task="${dependentId}"]`)).toBeVisible();
    await expect(notInRun.locator(`li[data-task="${draftId}"]`)).toBeVisible();
    await expect(metro.locator('[data-stack="out"]').locator(`button[data-task="${rootId}"]`)).toBeVisible();
    await expect(metro.locator('[data-stack="out"]').locator(`button[data-task="${dependentId}"]`)).toBeVisible();
    await shot(page, "running-list");

    // ── c. Zoom ──────────────────────────────────────────────────────────
    await lensToken.click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens).toBeVisible();
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "executor: running" })).toBeVisible();
    const terminal = lens.getByTestId("terminal-view");
    await expect(terminal).toContainText("stage=executor", { timeout: 10_000 });
    await expect(terminal).toContainText("attempt-one step 1", { timeout: 10_000 });
    // Live: more of the attempt's output arrives while the lens is open.
    const seen = await countText(terminal, "attempt-one step");
    expect(seen).toBeLessThan(ATTEMPT_ONE_LINES.length);
    await expect.poll(() => countText(terminal, "attempt-one step"), { timeout: 10_000 }).toBeGreaterThan(seen);
    await shot(page, "lens-live");

    // Task details: read-only while the pipeline works on it.
    await lens.getByRole("button", { name: "Task details" }).click();
    const sheet = page.getByRole("complementary").filter({ has: page.getByLabel("Title") });
    await expect(sheet.getByRole("note").filter({ hasText: "Locked while the pipeline works on it" })).toBeVisible();
    await expect(sheet.getByRole("button", { name: "Save" })).toHaveCount(0);
    await shot(page, "sheet-locked");

    // Esc closes one layer at a time: the sheet, then the lens.
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await expect(lens).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(lens).toBeHidden();
    await expect(group(panel, "Running")).toBeVisible();

    // ── d. Attempts ──────────────────────────────────────────────────────
    // Review fails the first time and sends the task back to the executor.
    await expect(metro.locator('[data-stack="stage:executor"]').locator(`button[data-task="${lensId}"]`)).toContainText(
      "#2",
      { timeout: 30_000 },
    );
    await lensToken.click();
    await expect(
      lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "executor: running, 2 attempts" }),
    ).toBeVisible();
    await expect(terminal).toContainText("attempt-two step 1", { timeout: 10_000 });
    // The loop-back attempt carries the review's feedback.
    await expect(lens.getByTestId("handoff")).toContainText("code_review attempt 1");
    await expect(lens.getByTestId("handoff")).toContainText(REVIEW_FAILURE);
    const attemptPicker = lens.getByRole("button", { name: /attempt 2 of 2/ });
    await expect(attemptPicker).toHaveText(/attempt 2 of 2 · live/);
    await attemptPicker.click();
    await page.getByRole("option", { name: /attempt 1 of 2/ }).click();
    await expect(lens.getByRole("button", { name: /attempt 1 of 2/ })).toBeVisible();
    // Attempt 1's finished log, all of it, and none of the live attempt's.
    await expect(terminal).toContainText(ATTEMPT_ONE_LINES[ATTEMPT_ONE_LINES.length - 1], { timeout: 10_000 });
    await expect(terminal).not.toContainText("attempt-two step");
    await shot(page, "lens-attempt-1");
  });

  test("graph focus, then the finished run from the run menu", async ({ page, routerURL }) => {
    test.setTimeout(90_000);
    await pollEventOfType(routerURL, sessionId, "pipeline.completed", 60_000);
    const panel = await openTasksTab(page, routerURL);

    // A finished run: its outcome in the bar, and New run.
    await expect(runTrigger(panel)).toContainText("Run 1");
    // The left-out root's dependent is reported skipped (contract B1), so the
    // run reads incomplete even though everything it ran is done.
    await expect(runTrigger(panel)).toContainText("incomplete");
    await expect(panel.getByRole("button", { name: "New run", exact: true })).toBeVisible();
    await expect(group(panel, "Done").locator(`li[data-task="${lensId}"]`)).toBeVisible();

    // ── e. Graph ─────────────────────────────────────────────────────────
    await panel.getByRole("radio", { name: "Graph" }).click();
    await panel.locator(".react-flow__node").filter({ hasText: JOURNEY_LENS_TASK }).click();
    const focus = panel.getByRole("region", { name: `Focus: ${JOURNEY_LENS_TASK}` });
    await expect(focus).toBeVisible();
    await expect(focus.getByRole("navigation", { name: "Breadcrumb" })).toContainText("Run 1");
    await expect(focus.getByTestId("task-lens")).toBeVisible();
    await shot(page, "graph-focus");
    await page.keyboard.press("Escape");
    await expect(focus).toBeHidden();
    await expect(panel.locator(".react-flow__node").filter({ hasText: JOURNEY_LENS_TASK })).toBeVisible();

    // ── f. Past run ──────────────────────────────────────────────────────
    await panel.getByRole("radio", { name: "List" }).click();
    await runTrigger(panel).click();
    await page.getByRole("menu", { name: "Runs" }).getByRole("menuitemradio", { name: /New run/ }).click();
    await expect(runTrigger(panel)).toHaveText(/New run/);
    // The root and its dependent are back in; the lens task is done.
    await expect(panel.getByRole("button", { name: /^Run \d+ tasks?$/ })).toHaveText(/Run 2 tasks/);

    await runTrigger(panel).click();
    await page.getByRole("menu", { name: "Runs" }).getByRole("menuitemradio", { name: /^Run 1/ }).click();
    await expect(runTrigger(panel)).toContainText("Run 1");
    await expect(runTrigger(panel)).toContainText("incomplete");
    await expect(group(panel, "Done").locator(`li[data-task="${lensId}"]`)).toBeVisible();
    await panel.getByTestId("metro").locator(`button[data-task="${lensId}"]`).click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "executor: done, 2 attempts" })).toBeVisible();
    await expect(lens.getByRole("list", { name: "Stages" }).getByRole("button", { name: "code_review: done, 2 attempts" })).toBeVisible();
    await shot(page, "past-run");
  });

  test("a run started here can be cancelled after a reload", async ({ page, routerURL }) => {
    test.setTimeout(90_000);
    // ── g. Cancel after reload ───────────────────────────────────────────
    let panel = await openTasksTab(page, routerURL);
    await panel.getByRole("button", { name: "New run", exact: true }).click();
    await panel.getByRole("button", { name: "Run 2 tasks" }).click();
    await expect(runTrigger(panel)).toContainText("Run 2");
    await expect(runTrigger(panel)).toContainText("running");
    await expect(
      panel.getByTestId("metro").locator('[data-stack="stage:executor"]').locator(`button[data-task="${rootId}"]`),
    ).toBeVisible({ timeout: 15_000 });

    await page.reload();
    await page.locator('[aria-label="Session list"]').getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();
    panel = page.locator("#workspace-panel-tasks");
    await expect(runTrigger(panel)).toContainText("Run 2");
    await expect(runTrigger(panel)).toContainText("running");
    const cancel = panel.getByRole("button", { name: "Cancel run" });
    await expect(cancel).toBeEnabled();
    await cancel.click();
    await expect(runTrigger(panel)).toContainText("cancelled", { timeout: 30_000 });
    await expect(panel.getByRole("button", { name: "New run", exact: true })).toBeVisible();

    // + Add task creates a draft and opens its sheet (it can't join a run
    // until it's confirmed).
    await panel.getByRole("button", { name: /\+ Add task/ }).click();
    const sheet = page.getByRole("complementary").filter({ has: page.getByLabel("Title") });
    await expect(sheet.getByLabel("Title")).toHaveValue("New task");
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await expect(group(panel, "Not in this run").getByText("New task", { exact: true })).toBeVisible();
  });
});
