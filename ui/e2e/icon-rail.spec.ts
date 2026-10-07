import { test, expect } from "./fixtures";

test("icon rail: hover peek overlays content instead of shifting it", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const rail = page.getByRole("navigation", { name: "Main navigation" });

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

  const rail = page.getByRole("navigation", { name: "Main navigation" });
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

  const rail = page.getByRole("navigation", { name: "Main navigation" });
  await rail.getByRole("button", { name: "Sessions" }).focus();
  await page.keyboard.press("Tab");
  await expect(rail).toHaveCSS("width", "176px");
});

test("icon rail: at rest, each icon is centred in its highlight and the highlight on the rail", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const rail = page.getByRole("navigation", { name: "Main navigation" });
  await expect(rail).toHaveCSS("width", "44px");
  await expect(rail.getByRole("button", { name: /rail/i })).toHaveCount(0);

  const railBox = (await rail.boundingBox())!;
  const item = rail.getByRole("button", { name: "Sessions" });
  const itemBox = (await item.boundingBox())!;
  const iconBox = (await item.locator("span").first().boundingBox())!;

  const mid = (b: { x: number; width: number }) => b.x + b.width / 2;
  // the rail's 1px right border isn't part of the column the eye centres on.
  expect(Math.abs(mid(itemBox) - (railBox.x + (railBox.width - 1) / 2))).toBeLessThanOrEqual(1);
  expect(Math.abs(mid(iconBox) - mid(itemBox))).toBeLessThanOrEqual(1);

  await rail.screenshot({ path: test.info().outputPath("rail.png") });
});
