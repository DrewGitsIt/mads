import { test, expect } from "./fixtures";

test("icon rail: hover peek overlays content instead of shifting it", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const rail = page.locator("nav[data-collapsed]");
  await expect(rail).toHaveAttribute("data-collapsed", "true");

  // main-surface element outside the rail entirely — its position must be
  // unaffected by the rail's hover peek.
  const main = page.locator("main");
  const before = await main.boundingBox();
  expect(before).not.toBeNull();

  await rail.hover();
  // wait for the peek's width transition to actually land before asserting.
  await expect(rail).toHaveCSS("width", "176px");

  // labels are readable, above surrounding content, once peeked open.
  await expect(rail.getByText("Sessions", { exact: true })).toBeVisible();

  const during = await main.boundingBox();
  expect(during).toEqual(before);

  await main.hover();
  await expect(rail).toHaveCSS("width", "44px");

  const after = await main.boundingBox();
  expect(after).toEqual(before);
});

test("icon rail: clicking an item's label collapses the peek and it stays collapsed", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const rail = page.locator("nav[data-collapsed]");
  await rail.hover();
  await expect(rail).toHaveCSS("width", "176px");

  // click on the label, right of the 44px icon column — the collapse pulls
  // the rail's edge out from under the pointer.
  await rail.getByRole("button", { name: "Retro" }).click({ position: { x: 120, y: 10 } });

  await expect(rail).toHaveCSS("width", "44px");
  // the bug re-expanded it a beat later, so hold the assertion past the
  // 0.15s transition rather than catching the first frame at 44px.
  await page.waitForTimeout(500);
  await expect(rail).toHaveCSS("width", "44px");
});

test("icon rail: keyboard focus still peeks the collapsed rail open", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const rail = page.locator("nav[data-collapsed]");
  await rail.getByRole("button", { name: "Sessions" }).focus();
  await page.keyboard.press("Tab");
  await expect(rail).toHaveCSS("width", "176px");
});
