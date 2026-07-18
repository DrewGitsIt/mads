import { execFile } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { createSession, importTasks, setupWorkspace } from "./lib/seed";

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
