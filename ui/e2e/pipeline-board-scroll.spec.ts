import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { createSession, importTasks, runFakePipeline, setupWorkspace } from "./lib/seed";

// Wide enough to comfortably fit the full board (8 fixed-240px columns +
// gaps/padding, ~2030px) plus the icon rail + session rail chrome (~440px
// at most) — used to prove the fix is a no-op at viewports where the board
// already fits.
const WIDE_VIEWPORT = { width: 2700, height: 900 };
const NARROW_VIEWPORT = { width: 900, height: 800 };

test.describe("pipeline board: horizontal scroll instead of viewport clipping", () => {
  test.describe.configure({ mode: "serial" });

  const taskTitle = "e2e board scroll task";
  let sessionName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-board-scroll-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e board scroll journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    await importTasks(routerURL, session.id, [taskTitle]);

    // Synchronous run through the default template's full six stages
    // (architect, executor, build, code_review, merge, verify) — the
    // unscripted fake reply ("ok", no "STAGE: fail" marker) counts as a
    // pass for every agent stage (engine.go's FailMarker doc comment), and
    // setupWorkspace's no-op Makefile passes build/verify — so this reaches
    // "done" without a scenario file. The run finishes before the UI ever
    // mounts, so the board renders from usePipelineRunHistory's fold of the
    // completed run's events, which still carries every stage the run
    // touched (pipeline/state/usePipelineRunHistory.ts) — including merge
    // and verify, the columns this task's clipping bug hid.
    runFakePipeline({
      madsBin: process.env.MADS_BIN!,
      dataURL,
      sessionID: session.id,
      workspace: workspaceDir,
      worktreeRoot: path.join(workspaceDir, ".worktrees"),
      env: process.env,
    });
  });

  test("at a narrow viewport the board pans to reach every column and the page doesn't scroll", async ({
    page,
    routerURL,
  }) => {
    await page.setViewportSize(NARROW_VIEWPORT);
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    await expect(page.locator('[data-testid="stage-column-verify"]')).toBeVisible();

    const pageScrollsHorizontally = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(pageScrollsHorizontally).toBe(false);

    const board = page.locator('[data-testid="stage-column-__queued"]').locator("..");
    const boardNeedsToScroll = await board.evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(boardNeedsToScroll).toBe(true);

    await board.evaluate((el) => {
      el.scrollLeft = el.scrollWidth;
    });
    await expect(page.locator('[data-testid="stage-column-merge"]')).toBeInViewport();
    await expect(page.locator('[data-testid="stage-column-verify"]')).toBeInViewport();
  });

  test("at a narrow viewport the drill-in rail, run header, and tab chrome don't overflow", async ({
    page,
    routerURL,
  }) => {
    await page.setViewportSize(NARROW_VIEWPORT);
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    const cancelButton = page.getByRole("button", { name: "Cancel" });
    const cancelBox = await cancelButton.boundingBox();
    if (!cancelBox) throw new Error("RunHeader's Cancel button has no bounding box");
    expect(cancelBox.x + cancelBox.width).toBeLessThanOrEqual(NARROW_VIEWPORT.width);

    await page.getByRole("button", { name: taskTitle }).click();
    const drillIn = page.locator('[data-testid="resize-handle"]').locator("..");
    const drillInBox = await drillIn.boundingBox();
    if (!drillInBox) throw new Error("drill-in panel has no bounding box");
    expect(drillInBox.x + drillInBox.width).toBeLessThanOrEqual(NARROW_VIEWPORT.width);
  });

  test("at a wide viewport the board needs no scrolling and nothing changes", async ({ page, routerURL }) => {
    await page.setViewportSize(WIDE_VIEWPORT);
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    await expect(page.locator('[data-testid="stage-column-verify"]')).toBeVisible();

    const pageScrollsHorizontally = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(pageScrollsHorizontally).toBe(false);

    const board = page.locator('[data-testid="stage-column-__queued"]').locator("..");
    const boardNeedsToScroll = await board.evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(boardNeedsToScroll).toBe(false);
  });
});
