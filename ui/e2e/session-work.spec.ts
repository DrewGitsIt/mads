import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import {
  createSession,
  importTasks,
  linkDependency,
  recordFileChange,
  runFakePipeline,
  setupWorkspace,
} from "./lib/seed";

const changedFilePath = "src/example.ts";

test.describe("session workspace: tasks/files/pipeline tabs", () => {
  test.describe.configure({ mode: "serial" });

  let sessionId: string;
  let sessionName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;
    const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-session-work-"));
    setupWorkspace(workspaceDir);
    sessionName = `e2e session-work journey ${Date.now()}`;
    const session = await createSession(routerURL, { name: sessionName, provider: "fake" });
    sessionId = session.id;
    await importTasks(routerURL, sessionId, ["e2e session-work task"]);
    const [depTask, dependentTask] = await importTasks(routerURL, sessionId, [
      "e2e graph dep",
      "e2e graph dependent",
    ]);
    await linkDependency(routerURL, dependentTask.id, depTask.id);
    runFakePipeline({
      madsBin: process.env.MADS_BIN!,
      dataURL,
      sessionID: sessionId,
      workspace: workspaceDir,
      worktreeRoot: path.join(workspaceDir, ".worktrees"),
      env: process.env,
    });
    await recordFileChange(dataURL, sessionId, changedFilePath);
  });

  test("tasks tab shows imported tasks", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Tasks" }).click();
    await expect(page.getByText("e2e session-work task")).toBeVisible();
  });

  test("files tab shows the pipeline's changed files", async ({ page, routerURL }) => {
    // GET /sessions/{id}/commits is a documented, not-yet-built Router route
    // (route_manifest_test.go: "Files tab / merge-back (getCommits), not
    // built") — FilesTab calls it unconditionally on mount, so this 404 is
    // expected until that endpoint lands, not a regression in this journey.
    page.allowStatus("/commits");

    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Files" }).click();
    await expect(page.getByText(changedFilePath)).toBeVisible();
  });

  test("pipeline tab shows the completed run", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    await page.getByRole("tab", { name: "Pipeline" }).click();
    const doneColumn = page.getByText("Done", { exact: true }).locator("../..");
    await expect(doneColumn.getByText("e2e session-work task")).toBeVisible();
  });

  test("graph mode shows the unsequenced tray and a canvas node", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();
    await page.getByRole("tab", { name: "Tasks" }).click();
    await page.getByRole("button", { name: "Graph" }).click();

    await expect(page.getByText("Runs when a slot opens", { exact: false })).toBeVisible();
    await expect(page.getByText("e2e session-work task")).toBeVisible(); // tray
    await expect(page.locator(".react-flow__node").getByText("e2e graph dependent")).toBeVisible(); // canvas
  });
});
