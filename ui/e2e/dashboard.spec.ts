import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { createSession, importTasks, runFakePipeline, setupWorkspace } from "./lib/seed";

let sessionId: string;
let taskId: string;

test.beforeAll(async () => {
  const routerURL = process.env.MADS_ROUTER_URL!;
  const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-dashboard-"));
  setupWorkspace(workspaceDir);
  const session = await createSession(routerURL, { name: "e2e dashboard journey", provider: "fake" });
  sessionId = session.id;
  [{ id: taskId }] = await importTasks(routerURL, sessionId, ["e2e dashboard task"]);
  runFakePipeline({
    madsBin: process.env.MADS_BIN!,
    dataURL: process.env.MADS_DATA_URL!,
    sessionID: sessionId,
    workspace: workspaceDir,
    worktreeRoot: path.join(workspaceDir, ".worktrees"),
    env: process.env,
  });
});

test("dashboard journey: real pipeline run shows up in metrics", async ({ page, routerURL }) => {
  await page.goto(routerURL);
  // exact: true — the shared instance also has other specs' sessions in the
  // rail (e.g. "e2e dashboard journey"), whose accessible names are a
  // case-insensitive substring match for "Dashboard" under default matching.
  await page.getByRole("button", { name: "Dashboard", exact: true }).click();

  // providers/fake's CostUSD is always 0 (no scripted override), so the
  // real signal is that cost.ByDay isn't empty — mirrors e2esmoke's own
  // sawUsage check on token counts, not dollars.
  await expect(page.getByText("No cost data for this range.")).toHaveCount(0);

  // metrics/tasks is un-scoped (aggregate across every session in the shared
  // instance, per services/router/internal/api's client) — assert a
  // non-empty, real completion metric rather than an exact rate, since
  // other specs' in-flight pipeline runs can shift the aggregate under
  // fullyParallel.
  await expect(page.getByText(/\d+ tasks? · \d+% completion rate/)).toBeVisible();

  await expect(page.getByText("No pipeline runs recorded yet.")).toHaveCount(0);
  await expect(page.getByText(sessionId)).toBeVisible();

  await expect(page.getByText("Something went wrong")).toHaveCount(0);

  // Run history's View run opens that session's Tasks tab on that run.
  const runRow = page.getByRole("row").filter({ has: page.getByRole("cell", { name: sessionId, exact: true }) });
  await expect(runRow).toHaveCount(1);
  await runRow.getByRole("button", { name: "View run" }).click();
  await expect(page.getByRole("tab", { name: "Tasks", exact: true })).toHaveAttribute("aria-selected", "true");
  const panel = page.locator("#workspace-panel-tasks");
  const runTrigger = panel.locator('button[aria-haspopup="menu"]').first();
  await expect(runTrigger).toContainText("Run 1");
  await expect(runTrigger).toContainText("completed");
  await expect(
    panel.getByTestId("metro").locator('[data-stack="done"]').locator(`button[data-task="${taskId}"]`),
  ).toBeVisible();
});
