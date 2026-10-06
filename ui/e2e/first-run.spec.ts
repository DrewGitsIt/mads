import { chmodSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import { bootTopology, type Topology } from "./lib/topology";
import { startFastModelFixture, type FastModelFixture } from "./lib/fastModelFixture";
import { setupWorkspace } from "./lib/seed";

test.describe.configure({ mode: "serial" });

let fastModel: FastModelFixture;
let topology: Topology;
const originalPath = process.env.PATH;

/** One Claude Code conversation as it sits on disk: a user line, then an assistant reply. */
function writeClaudeConversation(home: string, project: string, id: string, cwd: string, prompt: string): void {
  const dir = path.join(home, ".claude", "projects", project);
  mkdirSync(dir, { recursive: true });
  const user = { type: "user", uuid: `u-${id}`, timestamp: "2026-09-01T10:00:00.000Z", cwd, entrypoint: "cli", sessionId: id, message: { role: "user", content: prompt } };
  const assistant = { type: "assistant", uuid: `a-${id}`, timestamp: "2026-09-01T10:00:05.000Z", cwd, entrypoint: "cli", sessionId: id, message: { id: "m", role: "assistant", model: "claude-opus-5", content: [{ type: "text", text: "ok" }], usage: { input_tokens: 1, output_tokens: 1 } } };
  writeFileSync(path.join(dir, `${id}.jsonl`), `${JSON.stringify(user)}\n${JSON.stringify(assistant)}\n`);
}

test.beforeAll(async () => {
  // The Router only offers to import a harness's history when preflight finds
  // that harness installed, so put a stub `claude` on the PATH the Router is
  // booted with (bootTopology copies process.env). Only `--version` is run.
  const binDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-claude-bin-"));
  const stub = path.join(binDir, "claude");
  writeFileSync(stub, "#!/bin/sh\necho '2.1.5 (Claude Code)'\n");
  chmodSync(stub, 0o755);
  process.env.PATH = `${binDir}${path.delimiter}${originalPath ?? ""}`;

  fastModel = await startFastModelFixture();
  topology = await bootTopology({ fastModelURL: fastModel.url });
  process.env.PATH = originalPath;
});

test.afterAll(async () => {
  await topology.teardown();
  await fastModel.close();
});

test("first run wizard onboards a fresh install", async ({ page }) => {
  const workspaceDir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-first-run-"));
  setupWorkspace(workspaceDir);

  // The scratch HOME is the Router's HOME, so these are the user's existing
  // Claude Code conversations: two in a folder that exists, one in a folder
  // that doesn't.
  writeClaudeConversation(topology.homeDir, "-e2e-live", "11111111-1111-4111-8111-111111111111", workspaceDir, "fix the flaky login test");
  writeClaudeConversation(topology.homeDir, "-e2e-live", "22222222-2222-4222-8222-222222222222", workspaceDir, "add pagination to the audit log");
  writeClaudeConversation(topology.homeDir, "-e2e-gone", "33333333-3333-4333-8333-333333333333", "/e2e/no/such/folder", "sketch the billing export");

  await page.goto(topology.routerURL);

  await page.getByRole("button", { name: "Get started" }).click();
  await page.getByRole("button", { name: "Continue" }).click();

  // Conversation import: counts, the folders (one gone), the privacy line.
  await expect(page.getByRole("heading", { name: "Import conversations" })).toBeVisible();
  await expect(page.getByText("Found 3 conversations across 2 folders from Claude Code.")).toBeVisible();
  await expect(page.getByText("folder no longer exists")).toBeVisible();
  await expect(page.getByText(/nothing is uploaded or copied anywhere/i)).toBeVisible();
  await page.getByRole("button", { name: "Import conversations" }).click();

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

  // The import created one dormant session per conversation, and only those.
  const sessions = (await (await fetch(`${topology.routerURL}/sessions`)).json()) as { name: string; imported_from?: string }[];
  const imported = sessions.filter((s) => s.imported_from).map((s) => s.imported_from);
  expect(imported.sort()).toEqual([
    "11111111-1111-4111-8111-111111111111",
    "22222222-2222-4222-8222-222222222222",
    "33333333-3333-4333-8333-333333333333",
  ]);
});
