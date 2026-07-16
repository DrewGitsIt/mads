import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import {
  createSession,
  importTasks,
  pollRetro,
  runFakePipeline,
  setupWorkspace,
  startRetro,
} from "./lib/seed";

test.beforeAll(async () => {
  const routerURL = process.env.MADS_ROUTER_URL!;
  const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-retro-"));
  setupWorkspace(workspaceDir);
  const session = await createSession(routerURL, { name: "e2e retro journey", provider: "fake" });
  await importTasks(routerURL, session.id, ["e2e retro task"]);
  runFakePipeline({
    madsBin: process.env.MADS_BIN!,
    dataURL: process.env.MADS_DATA_URL!,
    sessionID: session.id,
    workspace: workspaceDir,
    worktreeRoot: path.join(workspaceDir, ".worktrees"),
    env: process.env,
  });

  const retroId = await startRetro(routerURL, [session.id]);
  await pollRetro(routerURL, retroId);
});

test("retro journey: history list reads real, persisted DPA content", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  // exact: true — the shared instance also has other specs' sessions in the
  // rail (e.g. "e2e retro journey"), whose accessible names are a
  // case-insensitive substring match for "Retro" under default matching.
  await page.getByRole("button", { name: "Retro", exact: true }).click();

  await page.getByRole("button", { name: /\d+ DPAs/ }).click();

  // Content is the fastModelFixture's fixed DPA (lib/fastModelFixture.ts) —
  // proves the tab reads real, persisted retro content, not a fixture.
  await expect(page.getByText("e2e smoke problem")).toBeVisible();
  await expect(page.getByText("e2e smoke evidence")).toBeVisible();
  await expect(page.getByText("e2e smoke root cause")).toBeVisible();
  await expect(page.getByText("e2e smoke action")).toBeVisible();
  await expect(page.getByText("pipeline_template")).toBeVisible();
});
