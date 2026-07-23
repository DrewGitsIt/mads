import { test, expect } from "./fixtures";
import { createSession } from "./lib/seed";
import { SYNTHESIS_FIXTURE_TASK_TITLE } from "./lib/fastModelFixture";

test.describe("synthesis proposals: agent turn -> proposal card -> approve -> task in pane", () => {
  test.describe.configure({ mode: "serial" });

  test("a scripted proposal renders as a card, and approving it lands the task in the rail", async ({
    page,
    routerURL,
  }) => {
    const sessionName = `e2e synthesis journey ${Date.now()}`;
    await createSession(routerURL, { name: sessionName, provider: "fake" });

    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(sessionName, { exact: true }).click();

    const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
    await expect(composer).toBeVisible();
    // "sounds good" is one of synthwatch's confirmationPhrases
    // (services/router/internal/synthwatch/watcher.go) — a high-value trigger
    // that fires the synthesizer regardless of debounce tier, once the turn ends.
    await composer.fill("echo sounds good, let's go with that plan");
    await page.getByRole("button", { name: "Send" }).click();
    await expect(page.getByText("you said sounds good, let's go with that plan")).toBeVisible();

    // synthesis_debounce_ms defaults to 2000ms (pkg/contracts/config.go): the
    // router's synthwatch collapses the turn into one synthesis job after that
    // window, which hits the Fast Model fixture and persists a Proposal —
    // the rail's proposal store picks it up live over the shared event stream.
    await expect(page.getByText(SYNTHESIS_FIXTURE_TASK_TITLE)).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("New task")).toBeVisible();

    await page.getByRole("button", { name: "Approve" }).click();
    await expect(page.getByRole("button", { name: "Approve" })).not.toBeVisible();

    // The proposal resolves into the real task, which the rail's Tasks list
    // (the same useTaskStore, live event-fed) must show without a page reload.
    await expect(page.getByText(SYNTHESIS_FIXTURE_TASK_TITLE)).toBeVisible();
  });
});
