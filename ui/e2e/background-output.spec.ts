import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { createSession } from "./lib/seed";

/**
 * A call that keeps running in the background (a run_in_background Bash):
 * its output streams into the tool dock while it runs, stays readable after
 * it exits, and says so plainly once its file is gone. Driven by the fake's
 * "background" dialect (providers/fake/background.go), in Claude Code's
 * shape and in Codex's, through the real session service, router stream
 * and dock.
 */

const COMPOSER = "Message the agent — @ to mention, / for commands";
const uniq = () => `${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
const conversation = (page: Page) =>
  page.locator("#workspace-panel-conversation");
const dock = (page: Page) => page.locator("[data-activity-dock]");

async function openSessionAndSend(
  page: Page,
  routerURL: string,
  prompt: string,
) {
  const name = `bg ${uniq()}`;
  await createSession(routerURL, {
    name,
    provider: "fake",
    isolation: "current_dir",
    // A background process is a command: limited trust (the default)
    // refuses it in the fake, as it asks first in a real harness.
    trust: "full",
  });
  await page.goto(routerURL);
  await page
    .locator('[aria-label="Session list"]')
    .getByText(name, { exact: true })
    .click();
  const composer = page.getByPlaceholder(COMPOSER);
  await expect(composer).toBeVisible();
  await composer.fill(prompt);
  await composer.press("Enter");
}

/** Opens the background call's row in the dock, unfolding its group if the turn already folded it. */
async function openCall(page: Page, title: string) {
  const row = conversation(page)
    .getByTestId("activity-row")
    .filter({ hasText: title });
  const group = conversation(page).getByTestId("activity-group").last();
  await expect(group).toBeVisible({ timeout: 15_000 });
  if (!(await row.isVisible())) await group.getByRole("button").first().click();
  await row.locator("[data-dock-source]").click();
  await expect(dock(page)).toBeVisible();
}

const terminalText = (page: Page) => dock(page).locator(".xterm-rows");

test.describe("background process output", () => {
  test.describe.configure({ mode: "serial" });

  test("streams while it runs and stays readable after it exits", async ({
    page,
    routerURL,
  }) => {
    test.setTimeout(60_000);
    await openSessionAndSend(page, routerURL, "background 8 500");
    await openCall(page, "background 8 500");

    await expect(dock(page).getByText("output · running")).toBeVisible({
      timeout: 10_000,
    });
    await expect(terminalText(page)).toContainText("line 1", {
      timeout: 10_000,
    });
    // Still mid-run: the later lines haven't arrived yet.
    await expect(terminalText(page)).not.toContainText("line 8");

    await expect(terminalText(page)).toContainText("[exited with code 0]", {
      timeout: 15_000,
    });
    await expect(dock(page).getByText("output · exited")).toBeVisible();
    await expect(terminalText(page)).toContainText("line 8");
  });

  test("a codex-shaped call streams the same way", async ({
    page,
    routerURL,
  }) => {
    await openSessionAndSend(page, routerURL, "background 2 0 codex");
    await openCall(page, "background 2 0 codex");
    await expect(terminalText(page)).toContainText(
      "Process exited with code 0",
      { timeout: 10_000 },
    );
    await expect(terminalText(page)).toContainText("line 2");
    await expect(dock(page).getByText("output · exited")).toBeVisible();
  });

  test("says when the output file has been removed", async ({
    page,
    routerURL,
  }) => {
    await openSessionAndSend(page, routerURL, "background 1 0 gone");
    // Let it finish first: the fake removes the file before the process
    // leaves the running list, which is what clears "running · …".
    await expect(
      conversation(page).getByText("started it in the background"),
    ).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(/^running · /)).toHaveCount(0, {
      timeout: 10_000,
    });
    await openCall(page, "background 1 0 gone");
    await expect(dock(page).getByTestId("background-output-gone")).toHaveText(
      "process output file removed",
      {
        timeout: 10_000,
      },
    );
  });
});
