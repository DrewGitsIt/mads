import { test, expect } from "./fixtures";
import { createSession, pollTranscriptContains, sendChatMessage } from "./lib/seed";

const COMPOSER = "Message the agent — @ to mention, / for commands";

test.describe("session commands: /usage", () => {
  test.describe.configure({ mode: "serial" });

  let id: string;
  let name: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    name = `e2e usage ${Date.now()}`;
    const s = await createSession(routerURL, { name, provider: "fake", isolation: "current_dir" });
    id = s.id;
    // One finished turn, so the report has context and token sections.
    await sendChatMessage(routerURL, id, "echo usage one");
    await pollTranscriptContains(routerURL, id, "you said usage one");
  });

  async function openSession(page: import("@playwright/test").Page, routerURL: string) {
    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    await expect(page.locator('[title="Rename session"]')).toHaveText(name);
  }

  test("/usage opens the Ledger; it updates live and Esc dismisses it", async ({ page, routerURL }) => {
    await openSession(page, routerURL);
    const input = page.getByPlaceholder(COMPOSER);
    await input.fill("/usage");
    await input.press("Enter");

    const card = page.getByTestId("usage-card");
    await expect(card).toBeVisible();
    for (const section of ["Health", "Context", "Tokens · session"]) {
      await expect(card.getByRole("heading", { name: section })).toBeVisible();
    }
    await expect(card.getByText(/^fake/)).toBeVisible();
    // The fake provider compacts natively.
    await expect(card.getByRole("button", { name: "Compact now" })).toBeVisible();
    // Never billed: neither plan limits nor spend.
    await expect(card.getByRole("heading", { name: /Plan limits|Spend/ })).toHaveCount(0);
    const turns = card.locator("div", { has: page.getByText("Turns", { exact: true }) }).last();
    await expect(turns).toHaveText(/^Turns\s*1$/);
    await card.screenshot({ path: "test-results/usage-ledger.png" });

    // Another turn while the card is open lands on it without reopening.
    await sendChatMessage(routerURL, id, "echo usage two");
    await expect(turns).toHaveText(/^Turns\s*2$/);

    await input.press("Escape");
    await expect(card).not.toBeVisible();
  });

  test("/status (an alias) opens the same card", async ({ page, routerURL }) => {
    await openSession(page, routerURL);
    const input = page.getByPlaceholder(COMPOSER);
    await input.fill("/status");
    await input.press("Enter");
    await expect(page.getByTestId("usage-card").getByRole("heading", { name: "Health" })).toBeVisible();
    await expect(input).toHaveValue("");
  });
});
