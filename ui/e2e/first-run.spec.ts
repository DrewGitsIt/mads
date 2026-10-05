import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { bootTopology, type Topology } from "./lib/topology";
import { startFastModelFixture, type FastModelFixture } from "./lib/fastModelFixture";
import { setupWorkspace } from "./lib/seed";

test.describe.configure({ mode: "serial" });

let fastModel: FastModelFixture;
let topology: Topology;

test.beforeAll(async () => {
  fastModel = await startFastModelFixture();
  topology = await bootTopology({ fastModelURL: fastModel.url });
});

test.afterAll(async () => {
  await topology.teardown();
  await fastModel.close();
});

test("first run wizard onboards a fresh install", async ({ page }) => {
  const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-first-run-"));
  setupWorkspace(workspaceDir);

  await page.goto(topology.routerURL);

  await page.getByRole("button", { name: "Get started" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  // The credentials step offers "Skip for now" only when it found env vars
  // to import, and a plain "Continue" when it found none — which depends on
  // the environment the suite runs in, so accept either.
  await expect(page.getByRole("heading", { name: "Import credentials" })).toBeVisible();
  await page.getByRole("button", { name: /^(Skip for now|Continue)$/ }).click();

  await page.locator("#workspace-name").fill("e2e first-run workspace");
  await page.locator("#workspace-path").fill(workspaceDir);
  await page.locator("#workspace-provider").selectOption("fake");
  await page.getByRole("button", { name: "Create workspace" }).click();

  await page.getByRole("button", { name: "Go to Launchpad" }).click();

  await expect(page.getByLabel("Describe your goal")).toBeVisible();

  const cfg = (await (await fetch(`${topology.routerURL}/config`)).json()) as {
    onboarding_complete: boolean;
    default_workspace?: string;
    default_provider?: string;
  };
  expect(cfg.onboarding_complete).toBe(true);
  expect(cfg.default_workspace).toBeTruthy();
  expect(cfg.default_provider).toBe("fake");
});
