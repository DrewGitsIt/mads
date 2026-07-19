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
