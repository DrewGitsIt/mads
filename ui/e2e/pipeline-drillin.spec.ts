import { execFile } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { createSession, importTasks, pollEventOfType, setupWorkspace } from "./lib/seed";

test.describe("pipeline drill-in: live output + resize persistence", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e drill-in task";
  let sessionName: string;
  let runExit: Promise<void>;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-drillin-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e drill-in journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    await importTasks(routerURL, session.id, [taskTitle]);

    // Every stage's prompt gets a deliberate multi-chunk delay — a real
    // window to observe "streaming," not an instant no-op (HANDOFF.md §6).
    // Applied broadly (not just to one named stage) because the UI's own
    // navigation (goto, click session, click tab, click task) takes an
    // unpredictable amount of wall-clock time — a single fast stage could
    // finish before the test ever gets to look at it.
    const scenarioPath = path.join(workspaceDir, "scenario.yaml");
    writeFileSync(
      scenarioPath,
      [
        "name: drill-in",
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

    const worktreeRoot = path.join(workspaceDir, ".worktrees");
    mkdirSync(worktreeRoot, { recursive: true });

    // Async, unlike lib/seed.ts's runFakePipeline (execFileSync) — this
    // journey needs to observe the run *while it's in flight*, not block
    // the whole beforeAll until it's done.
    runExit = new Promise((resolve, reject) => {
      execFile(
        process.env.MADS_BIN!,
        [
          "run",
          "-data",
          dataURL,
          "-session",
          session.id,
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
  });

  test.afterAll(async () => {
    await runExit;
  });

  test("drill-in shows live streaming agent output for a running task", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    await page.getByRole("button", { name: taskTitle }).click();
    const terminal = page.locator('[data-testid="terminal-view"]');
    await expect(terminal).toBeVisible();
    // xterm's DOM renderer splits a line across several inline nodes —
    // toContainText checks the container's aggregate textContent, unlike
    // getByText, which expects one element/text node holding the whole string.
    await expect(terminal).toContainText("Reading task context", { timeout: 10_000 });
    await expect(terminal).toContainText("Editing engine.go", { timeout: 10_000 });
  });

  test("drill-in panel resize persists across reload", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();
    await page.getByRole("button", { name: taskTitle }).click();

    const handle = page.locator('[data-testid="resize-handle"]');
    const panel = handle.locator("..");
    const box = await handle.boundingBox();
    if (!box) throw new Error("resize handle has no bounding box");

    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2, box.y - 150, { steps: 10 });
    await page.mouse.up();

    const draggedHeight = await panel.evaluate((el) => (el as HTMLElement).style.height);
    const stored = await page.evaluate(() => localStorage.getItem("mads.pipeline.drillInHeight"));
    expect(stored).toBe(String(parseInt(draggedHeight, 10)));

    await page.reload();
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();
    await page.getByRole("button", { name: taskTitle }).click();

    const reopenedPanel = page.locator('[data-testid="resize-handle"]').locator("..");
    const reopenedHeight = await reopenedPanel.evaluate((el) => (el as HTMLElement).style.height);
    expect(reopenedHeight).toBe(draggedHeight);
  });
});

test.describe("pipeline drill-in: resume mid-run on mount", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e resume task";
  let sessionName: string;
  let sessionId: string;
  let runExit: Promise<void>;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-resume-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e resume journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    sessionId = session.id;
    await importTasks(routerURL, session.id, [taskTitle]);

    // A deliberate multi-chunk delay per stage — enough wall-clock time to
    // poll for the run being mid-stage, then navigate the UI to mount the
    // tab *after* it's already in flight (the arc this journey proves,
    // pipeline/HANDOFF.md §6), and later observe a live stage transition.
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

    const worktreeRoot = path.join(workspaceDir, ".worktrees");
    mkdirSync(worktreeRoot, { recursive: true });

    // Async (not execFileSync) so the run stays in flight while the test
    // starts it, waits for a stage event, then navigates — same pattern as
    // the "live output" block above.
    runExit = new Promise((resolve, reject) => {
      execFile(
        process.env.MADS_BIN!,
        [
          "run",
          "-data",
          dataURL,
          "-session",
          session.id,
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

    // Wait until the run has actually emitted a stage event before this
    // test ever navigates to the tab — mounting *after* pipeline.started
    // already fired is exactly the bug this task fixes.
    await pollEventOfType(routerURL, sessionId, "stage.started");
  });

  test.afterAll(async () => {
    await runExit;
  });

  test("mounting mid-run resumes the live board, picker, and drill-in", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    // Board: task card is in its current stage's column, not Queued.
    const queuedColumn = page.locator('[data-testid="stage-column-__queued"]');
    await expect(queuedColumn.getByRole("button", { name: taskTitle })).not.toBeVisible();
    await expect(page.getByRole("button", { name: taskTitle })).toBeVisible();

    // Picker: reads "in progress", shown as the sole/selected entry.
    await expect(page.getByRole("button", { name: "Run history" })).toContainText("in progress");

    // Drill-in: stage timeline is populated from the resumed fold, not empty.
    await page.getByRole("button", { name: taskTitle }).click();
    await expect(page.getByText("No stages started yet.")).not.toBeVisible();
    await expect(page.getByTestId("stage-timeline-entry").first()).toBeVisible();

    // Live continuation: the scenario's own progression moves the card to
    // the next stage without any extra seeding from this test.
    const taskColumn = () =>
      page.locator('[data-testid^="stage-column-"]').filter({ has: page.getByRole("button", { name: taskTitle }) });
    const initialColumn = await taskColumn().first().getAttribute("data-testid");

    await expect(async () => {
      const currentColumn = await taskColumn().first().getAttribute("data-testid");
      expect(currentColumn).not.toBe(initialColumn);
    }).toPass({ timeout: 20_000 });
  });
});
