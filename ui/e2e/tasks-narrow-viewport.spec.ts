import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { createSession, importTasks, runFakePipeline, setupWorkspace } from "./lib/seed";

// The Tasks tab's run view at a narrow and a wide viewport: the metro draws
// every station of the template, and neither it, the run bar, nor a task's
// lens pushes the page into horizontal scrolling. (This replaced the
// Pipeline tab's board-scroll journey; the metro scales to the width
// instead of scrolling, so there's no board to pan.)
const WIDE_VIEWPORT = { width: 2700, height: 900 };
const NARROW_VIEWPORT = { width: 900, height: 800 };

// Every stage of the default template the run went through, plus the end.
const STATIONS = ["architect", "executor", "build", "code_review", "merge", "verify", "Done"];

test.describe("tasks tab: run view fits the viewport", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e board scroll task";
  let sessionName: string;
  let taskId: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-board-scroll-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e board scroll journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    [{ id: taskId }] = await importTasks(routerURL, session.id, [taskTitle]);

    // A synchronous run through the default template's six stages — the
    // unscripted fake reply passes every agent stage, and setupWorkspace's
    // no-op Makefile passes build/verify — so it finishes before the UI
    // mounts and the tab shows it as the newest past run.
    runFakePipeline({
      madsBin: process.env.MADS_BIN!,
      dataURL,
      sessionID: session.id,
      workspace: workspaceDir,
      worktreeRoot: path.join(workspaceDir, ".worktrees"),
      env: process.env,
    });
  });

  async function openTasks(page: Page, routerURL: string) {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks", exact: true }).click();
    const panel = page.locator("#workspace-panel-tasks");
    await expect(panel.locator('button[aria-haspopup="menu"]').first()).toContainText("completed");
    return panel;
  }

  const pageScrollsHorizontally = (page: Page) =>
    page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);

  test("at a narrow viewport every station is on screen and the page doesn't scroll", async ({ page, routerURL }) => {
    await page.setViewportSize(NARROW_VIEWPORT);
    const panel = await openTasks(page, routerURL);
    const metro = panel.getByTestId("metro");
    for (const name of STATIONS) {
      await expect(metro.locator(`[data-station="${name}"]`)).toBeInViewport();
    }
    await expect(metro.locator('[data-stack="done"]').locator(`button[data-task="${taskId}"]`)).toBeInViewport();
    expect(await pageScrollsHorizontally(page)).toBe(false);
  });

  test("at a narrow viewport the run bar and the lens don't overflow", async ({ page, routerURL }) => {
    await page.setViewportSize(NARROW_VIEWPORT);
    const panel = await openTasks(page, routerURL);

    const newRun = panel.getByRole("button", { name: "New run", exact: true });
    const newRunBox = await newRun.boundingBox();
    if (!newRunBox) throw new Error("the run bar's New run button has no bounding box");
    expect(newRunBox.x + newRunBox.width).toBeLessThanOrEqual(NARROW_VIEWPORT.width);

    await panel.getByTestId("metro").locator(`button[data-task="${taskId}"]`).click();
    const lens = panel.getByTestId("task-lens");
    await expect(lens).toBeVisible();
    const lensBox = await lens.boundingBox();
    if (!lensBox) throw new Error("the task lens has no bounding box");
    expect(lensBox.x + lensBox.width).toBeLessThanOrEqual(NARROW_VIEWPORT.width);
    expect(await pageScrollsHorizontally(page)).toBe(false);
  });

  test("at a wide viewport nothing scrolls either", async ({ page, routerURL }) => {
    await page.setViewportSize(WIDE_VIEWPORT);
    const panel = await openTasks(page, routerURL);
    const metro = panel.getByTestId("metro");
    for (const name of STATIONS) {
      await expect(metro.locator(`[data-station="${name}"]`)).toBeInViewport();
    }
    expect(await pageScrollsHorizontally(page)).toBe(false);
  });
});
