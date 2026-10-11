import { test, expect } from "./fixtures";

test("settings journey: appearance persists, preflight reports live data service", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  await page.getByRole("button", { name: "Settings" }).click();

  const palette = page.getByRole("radiogroup", { name: "Palette" });
  const theme = page.getByRole("radiogroup", { name: "Theme" });

  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith("/config")),
    palette.getByRole("radio", { name: "Space" }).click(),
  ]);
  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith("/config")),
    theme.getByRole("radio", { name: "Light" }).click(),
  ]);

  const html = page.locator("html");
  await expect(html).toHaveAttribute("data-palette", "space");
  await expect(html).toHaveAttribute("data-theme", "light");

  // App.tsx applies the fetched config's theme/palette on boot (before any
  // navigation) — reload and check the root attributes directly, proving
  // the PUT /config round-trip persisted rather than just in-memory state.
  await page.reload();
  await expect(html).toHaveAttribute("data-palette", "space");
  await expect(html).toHaveAttribute("data-theme", "light");

  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByRole("button", { name: "System" }).click();

  // The Router wires the real preflight.New checker (not StubChecker —
  // services/router/main.go), so GET /preflight genuinely checks the live
  // Data Service. Its check id is "data_service"; the row reads "Data service".
  const dataServiceRow = page.locator('li[data-preflight-check="data_service"]');
  await expect(dataServiceRow).toBeVisible();
  await expect(dataServiceRow).toContainText("✓");
  await expect(dataServiceRow).toContainText("Data service");
});

// One save bar for the page: draft edits wait for it, while live controls
// (theme) persist only their own field and never commit the draft with them.
test("settings journey: live theme change doesn't commit an unsaved Editor edit", async ({
  page,
  routerURL,
}) => {
  const getConfig = async () => (await page.request.get(`${routerURL}/config`)).json();
  const isConfigPut = (r: { request(): { method(): string }; url(): string }) =>
    r.request().method() === "PUT" && r.url().endsWith("/config");

  await page.goto(routerURL);
  await page.getByRole("button", { name: "Settings" }).click();

  const editor = page.getByLabel("Open files with");
  const bar = page.getByRole("region", { name: "Unsaved changes" });
  await expect(bar).toHaveCount(0);

  await editor.fill("code -g");
  await expect(bar).toBeVisible();
  await expect(bar).toContainText("1 setting");

  const html = page.locator("html");
  const nextTheme = (await html.getAttribute("data-theme")) === "light" ? "Dark" : "Light";
  await Promise.all([
    page.waitForResponse(isConfigPut),
    page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: nextTheme }).click(),
  ]);
  await expect(bar).toContainText("1 setting");

  const afterTheme = await getConfig();
  expect(afterTheme.theme).toBe(nextTheme.toLowerCase());
  expect(afterTheme.editor_command ?? "").not.toBe("code -g");

  await Promise.all([page.waitForResponse(isConfigPut), bar.getByRole("button", { name: "Save" }).click()]);
  await expect(bar).toHaveCount(0);

  await page.reload();
  await expect(html).toHaveAttribute("data-theme", nextTheme.toLowerCase());
  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.getByLabel("Open files with")).toHaveValue("code -g");

  // Leave the shared backend as found for later specs.
  await page.getByLabel("Open files with").fill("");
  await Promise.all([page.waitForResponse(isConfigPut), bar.getByRole("button", { name: "Save" }).click()]);
});
