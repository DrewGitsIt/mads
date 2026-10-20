import { execFile } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { createSession, importTasks, pollEventOfType, pollStageStarted, setupWorkspace } from "./lib/seed";

// A task's lens in the Tasks tab (docs/TASKS-PIPELINE-DESIGN.md) shows the
// selected stage attempt's output while the run is still writing it. These
// journeys run the pipeline through `mads run` (async, so the test can watch
// it in flight) with a per-journey fake scenario.

/** Starts `mads run` for the session in the background; resolves when it exits. */
function startRun(sessionId: string, workspaceDir: string, scenarioPath: string): Promise<void> {
  const worktreeRoot = path.join(workspaceDir, ".worktrees");
  mkdirSync(worktreeRoot, { recursive: true });
  return new Promise((resolve, reject) => {
    execFile(
      process.env.MADS_BIN!,
      [
        "run",
        "-data",
        process.env.MADS_DATA_URL!,
        "-session",
        sessionId,
        "-workspace",
        workspaceDir,
        "-provider",
        "fake",
        "-fake-scenario",
        scenarioPath,
        "-stage-timeout",
        "30s",
        "-worktree-root",
        worktreeRoot,
      ],
      { env: process.env },
      (err) => (err ? reject(err) : resolve()),
    );
  });
}

/**
 * Opens the session's Tasks tab and zooms into one task: its lens (stage
 * strip, timeline, the selected attempt's output). Through the list row's
 * title, which is there whatever stage the task is at.
 */
async function openLens(page: Page, routerURL: string, sessionName: string, taskId: string) {
  await page.goto(routerURL);
  const rail = page.locator('[aria-label="Session list"]');
  await rail.getByText(sessionName, { exact: true }).click();
  await page.getByRole("tab", { name: "Tasks", exact: true }).click();
  const panel = page.locator("#workspace-panel-tasks");
  await panel.locator(`li[data-task="${taskId}"]`).getByRole("button").first().click();
  const lens = panel.getByTestId("task-lens");
  await expect(lens).toBeVisible();
  return { panel, lens };
}

test.describe("task lens: live raw output", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e drill-in task";
  let sessionName: string;
  let taskId: string;
  let runExit: Promise<void>;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-drillin-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e drill-in journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    [{ id: taskId }] = await importTasks(routerURL, session.id, [taskTitle]);

    // Every stage's prompt gets a deliberate multi-chunk delay — a real
    // window to observe "streaming," not an instant no-op (HANDOFF.md §6).
    // Applied broadly (not just to one named stage) because the UI's own
    // navigation takes an unpredictable amount of wall-clock time — a
    // single fast stage could finish before the test ever gets to look.
    const scenarioPath = path.join(workspaceDir, "scenario.yaml");
    writeFileSync(
      scenarioPath,
      [
        "name: drill-in",
        "rules:",
        `  - match: "Task: ${taskTitle}"`,
        "    tools:",
        "      - name: Read",
        "        path: engine.go",
        "    chunks:",
        '      - "Reading task context...\\n"',
        '      - "Editing engine.go...\\n"',
        '      - "Running tests...\\n"',
        '      - "STAGE: pass"',
        "    delay_ms: 800",
        "",
      ].join("\n"),
    );
    runExit = startRun(session.id, workspaceDir, scenarioPath);
    await pollEventOfType(routerURL, session.id, "stage.started");
  });

  test.afterAll(async () => {
    await runExit;
  });

  test("the lens streams a running task's output as it's written", async ({ page, routerURL }) => {
    const { lens } = await openLens(page, routerURL, sessionName, taskId);
    const terminal = lens.getByTestId("terminal-view");
    await expect(terminal).toBeVisible();
    // xterm's DOM renderer splits a line across several inline nodes —
    // toContainText checks the container's aggregate textContent, unlike
    // getByText, which expects one element/text node holding the whole string.
    // The structured log format (services/pipeline/engine/outputlog.go):
    // header line, tool-use marker, then the streamed chunks.
    await expect(terminal).toContainText(/stage=(architect|executor|code_review)/, { timeout: 10_000 });
    await expect(terminal).toContainText(`⏺ Read engine.go`, { timeout: 10_000 });
    await expect(terminal).toContainText("Reading task context", { timeout: 10_000 });
    await expect(terminal).toContainText("Editing engine.go", { timeout: 10_000 });
  });
});

test.describe("task lens: normalized turns feed", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e feed task";
  let sessionName: string;
  let taskId: string;
  let runExit: Promise<void>;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-feed-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e feed journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    [{ id: taskId }] = await importTasks(routerURL, session.id, [taskTitle]);

    // announce_one_shot_ref opts the fake into claude-code's shape: OneShot
    // announces a session ref and backs it with a transcript. This is the
    // only way to reach AgentFeedView end to end — the fake otherwise never
    // announces (the raw-output journey above exercises that fallback). The
    // scripted tool + delay only matches the "executor" stage; every other
    // agent stage falls through to the fake's instant "ok", so the wait
    // below can pin down exactly the window this test needs.
    const scenarioPath = path.join(workspaceDir, "scenario.yaml");
    writeFileSync(
      scenarioPath,
      [
        "name: drill-in-feed",
        "announce_one_shot_ref: true",
        "rules:",
        '  - match: "Pipeline stage: executor"',
        "    tools:",
        "      - name: Bash",
        '        input: \'{"command": "make test"}\'',
        "    chunks:",
        '      - "Running the test suite...\\n"',
        '      - "STAGE: pass"',
        "    delay_ms: 2000",
        "",
      ].join("\n"),
    );
    runExit = startRun(session.id, workspaceDir, scenarioPath);

    // Wait until the executor stage itself has started before navigating —
    // that's the multi-second scripted window this test needs to land in.
    await pollStageStarted(routerURL, session.id, "executor");
  });

  test.afterAll(async () => {
    await runExit;
  });

  test("the lens renders tool rows, timestamps, and a ticking liveness indicator from the normalized feed", async ({
    page,
    routerURL,
  }) => {
    const { lens } = await openLens(page, routerURL, sessionName, taskId);

    // The announced ref flips the lens's Turns view from the raw
    // AgentOutputView over to the normalized AgentFeedView.
    const feed = lens.getByTestId("agent-feed");
    await expect(feed).toBeVisible({ timeout: 10_000 });

    // Tool row: distinct from prose, shows the Bash command as its salient
    // argument, and carries a visible HH:MM:SS timestamp.
    const toolRow = lens.getByTestId("feed-tool-row").first();
    await expect(toolRow).toBeVisible();
    await expect(toolRow).toContainText("Bash");
    await expect(toolRow).toContainText("make test");
    await expect(toolRow).toContainText(/\d{2}:\d{2}:\d{2}/);

    // Liveness: the tool_use has no matching tool_result yet, so the row
    // ticks elapsed time with no new transcript entries required.
    const liveness = lens.getByTestId("feed-liveness");
    await expect(liveness).toBeVisible();
    const firstReading = await liveness.textContent();
    await expect(async () => {
      const reading = await liveness.textContent();
      expect(reading).not.toBe(firstReading);
    }).toPass({ timeout: 5_000 });

    // Prose rows (agent/user text) are visually and structurally distinct
    // from tool rows — a different testid entirely.
    await expect(lens.getByTestId("feed-prose-row").first()).toBeVisible({ timeout: 15_000 });
  });
});

test.describe("task lens: resume mid-run on mount", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e resume task";
  let sessionName: string;
  let taskId: string;
  let runExit: Promise<void>;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-resume-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e resume journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    [{ id: taskId }] = await importTasks(routerURL, session.id, [taskTitle]);

    // A deliberate multi-chunk delay per stage — enough wall-clock time to
    // mount the tab *after* the run is already in flight (the arc this
    // journey proves, pipeline/HANDOFF.md §6), and later observe a live
    // stage transition.
    const scenarioPath = path.join(workspaceDir, "scenario.yaml");
    writeFileSync(
      scenarioPath,
      [
        "name: resume",
        "rules:",
        `  - match: "Task: ${taskTitle}"`,
        "    chunks:",
        '      - "Reading task context...\\n"',
        '      - "Editing engine.go...\\n"',
        '      - "Running tests...\\n"',
        '      - "STAGE: pass"',
        "    delay_ms: 800",
        "",
      ].join("\n"),
    );
    runExit = startRun(session.id, workspaceDir, scenarioPath);

    // Mounting *after* pipeline.started already fired is the case under test.
    await pollEventOfType(routerURL, session.id, "stage.started");
  });

  test.afterAll(async () => {
    await runExit;
  });

  test("mounting mid-run resumes the run bar, the metro, and the lens", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();
    const panel = page.locator("#workspace-panel-tasks");

    // Run bar: the resumed run, live.
    const runTrigger = panel.locator('button[aria-haspopup="menu"]').first();
    await expect(runTrigger).toContainText("Run 1");
    await expect(runTrigger).toContainText("running");

    // Metro: the token sits at a stage station, not at the start.
    const metro = panel.getByTestId("metro");
    const token = metro.locator(`button[data-task="${taskId}"]`);
    await expect(token).toBeVisible();
    await expect(metro.locator('[data-stack="start"]').locator(`button[data-task="${taskId}"]`)).toHaveCount(0);
    const stackOf = () =>
      metro.locator("[data-stack]").filter({ has: page.locator(`button[data-task="${taskId}"]`) }).first().getAttribute("data-stack");
    const initialStack = await stackOf();
    expect(initialStack).toMatch(/^stage:/);

    // Lens: the timeline is populated from the resumed fold, not empty.
    await token.click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens).toBeVisible();
    await expect(lens.getByText("Not started.")).toHaveCount(0);
    await expect(lens.getByTestId("stage-timeline-entry").first()).toBeVisible();

    // Live continuation: the scenario's own progression moves the token to
    // the next station without any extra seeding from this test.
    await expect.poll(stackOf, { timeout: 20_000 }).not.toBe(initialStack);
  });
});
