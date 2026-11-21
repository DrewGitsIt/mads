import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { createSession } from "./lib/seed";
import { apiFetch } from "./lib/auth";

// Selection follows the list: when the open session goes — archived from the
// rail, deleted from its menu, checked off, deleted from outside — its
// neighbour in the rail (as filtered and searched) opens, else the Launchpad.
// Every test narrows the rail with a search for its own token, so cards from
// other specs sharing the topology can't be "the next card".

const rail = (page: Page) => page.locator('[aria-label="Session list"]');
const card = (page: Page, id: string) => rail(page).locator(`[data-rail-id="${id}"]`);
const selected = (page: Page) => rail(page).locator('[data-rail-card][aria-current="true"]');

async function seed(routerURL: string, token: string, n: number) {
  const out: { id: string; name: string }[] = [];
  for (let i = 0; i < n; i++) out.push(await createSession(routerURL, { name: `sel ${token} ${i}`, provider: "fake" }));
  return out;
}

async function narrowTo(page: Page, routerURL: string, token: string, count: number) {
  await page.goto(routerURL);
  await page.getByLabel("Search sessions").fill(token);
  await expect(rail(page).locator("[data-rail-card]")).toHaveCount(count);
  return rail(page)
    .locator("[data-rail-card]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-rail-id")!));
}

async function archive(page: Page, id: string) {
  await card(page, id).hover();
  await card(page, id).locator("[aria-label^='Archive']").click();
  await page.getByRole("alertdialog").getByRole("button", { name: /^((Close|Stop) and delete|Delete)$/ }).click();
}

test("archiving the open session opens its neighbour, then the previous, then the Launchpad", async ({ page, routerURL }) => {
  const token = `a${Date.now()}`;
  await seed(routerURL, token, 3);
  const order = await narrowTo(page, routerURL, token, 3);

  await card(page, order[1]).click();
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[1]);
  await archive(page, order[1]);
  await expect(card(page, order[1])).toHaveCount(0);
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[2]);

  // The last card: back to the previous one.
  await archive(page, order[2]);
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[0]);

  // The only one left: the Launchpad.
  await archive(page, order[0]);
  await expect(page.getByLabel("Describe your goal")).toBeVisible();
  await expect(selected(page)).toHaveCount(0);
});

test("Delete from the workspace menu opens the neighbour too", async ({ page, routerURL }) => {
  const token = `d${Date.now()}`;
  await seed(routerURL, token, 2);
  const order = await narrowTo(page, routerURL, token, 2);

  await card(page, order[0]).click();
  await page.getByLabel("Workspace actions").click();
  await page.getByRole("button", { name: /^Delete/ }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: /^((Close|Stop) and delete|Delete)$/ }).click();
  await expect(card(page, order[0])).toHaveCount(0);
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[1]);
});

test("✓ Done on the open session moves on to the next card", async ({ page, routerURL }) => {
  const token = `k${Date.now()}`;
  const sessions = await seed(routerURL, token, 2);
  const order = await narrowTo(page, routerURL, token, 2);
  const first = sessions.find((s) => s.id === order[0])!;

  await card(page, order[0]).click();
  await card(page, order[0]).hover();
  await page.getByRole("button", { name: `Check off ${first.name}` }).click();
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[1]);
});

test("a session deleted from outside (the CLI, another window) takes the pane with it", async ({ page, routerURL }) => {
  const token = `x${Date.now()}`;
  await seed(routerURL, token, 2);
  const order = await narrowTo(page, routerURL, token, 2);

  await card(page, order[0]).click();
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[0]);
  // Deleted elsewhere, the open conversation may read it once more before the
  // list catches up: that 404 is the signal, so it's expected here.
  page.allowStatus(`/sessions/${order[0]}`);
  const res = await apiFetch(`${routerURL}/sessions/${order[0]}`, { method: "DELETE" });
  expect(res.ok).toBe(true);
  await expect(selected(page)).toHaveAttribute("data-rail-id", order[1]);
});
