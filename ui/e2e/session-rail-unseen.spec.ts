import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";
import { createSession } from "./lib/seed";

/**
 * The unseen highlight against the live app: a session that appears while the
 * rail is open (here from the API, standing in for an import or a pipeline)
 * arrives lit, a real pointer clears it, and the cleared state survives a
 * reload. Each test's browser context starts with empty localStorage, so the
 * first load seeds whatever already exists as seen.
 */
test.describe("session rail unseen highlight", () => {
  test.describe.configure({ mode: "serial" });

  const stamp = `${Date.now()}`;
  const railOf = (page: Page) => page.locator('[aria-label="Session list"]');
  const cardFor = (page: Page, name: string) =>
    railOf(page).locator("[data-rail-card]").filter({ hasText: name }).first();

  test("a session that arrives live is lit until hovered, and stays cleared across a reload", async ({
    page,
    routerURL,
  }) => {
    const existing = `unseen existing ${stamp}`;
    const fresh = `unseen fresh ${stamp}`;
    await createSession(routerURL, { name: existing, provider: "fake" });

    await page.goto(routerURL);
    await expect(cardFor(page, existing)).toBeVisible();
    // First run: everything already there is seeded as seen.
    await expect(railOf(page).locator("[data-unseen]")).toHaveCount(0);

    await createSession(routerURL, { name: fresh, provider: "fake" });
    const card = cardFor(page, fresh);
    await expect(card).toBeVisible();
    await expect(card).toHaveAttribute("data-unseen", "");
    await expect(card).toContainText("New session");
    await expect(cardFor(page, existing)).not.toHaveAttribute("data-unseen", "");

    await card.hover();
    await expect(card).not.toHaveAttribute("data-unseen", "");
    await expect(card).not.toContainText("New session");

    await page.reload();
    await expect(cardFor(page, fresh)).toBeVisible();
    await expect(railOf(page).locator("[data-unseen]")).toHaveCount(0);
  });
});
