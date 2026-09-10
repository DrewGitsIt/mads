import { test, expect } from "./fixtures";

const SEEDED_AGENTS = [
  "synthesizer",
  "agent-builder",
  "architect",
  "executor",
  "code-reviewer",
  "verifier",
];

test("library journey: agents, capabilities, workspaces, templates", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  await page.getByRole("button", { name: "Library" }).click();
  // Drop focus and park the pointer on the main surface so the icon rail's
  // peek collapses: the rail expands on :hover OR :focus-within, and after
  // the click above the Library item still holds focus while Playwright's
  // pointer sits inside the expanded rail — which overlays the left-edge
  // shelf chips, so every click aimed there would land on the rail instead.
  await page.getByRole("button", { name: "Library" }).blur();
  await page.locator("main").hover();
  await expect(page.locator("nav[data-collapsed]")).toHaveCSS("width", "44px");

  // Agents shelf is the default — seeded by services/data at migration time
  // (0005_registries.up.sql), assert real names render, not a count. Each
  // card's button accessible name is name+"System"+quadrant label
  // concatenated (e.g. "executor" collides with the "Autonomous executor"
  // quadrant chip under substring matching) — anchor at the start instead.
  for (const name of SEEDED_AGENTS) {
    await expect(page.getByRole("button", { name: new RegExp(`^${name}`) })).toBeVisible();
  }

  await page.getByRole("button", { name: "Capabilities" }).click();
  await page.getByRole("button", { name: "New capability" }).click();
  const capabilityName = `e2e capability ${Date.now()}`;
  await page.getByLabel("Name").fill(capabilityName);
  await page.getByLabel("Path").fill("e2e/skill");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(capabilityName)).toBeVisible();

  await page.getByText(capabilityName).click();
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText(capabilityName)).toHaveCount(0);

  await page.getByRole("button", { name: "Workspaces" }).click();
  await expect(page.getByRole("heading", { name: "Workspaces" })).toBeVisible();

  await page.getByRole("button", { name: "Templates" }).click();
  await page.getByText("default-implementation").click();
  await expect(page.getByText("Edit default-implementation")).toBeVisible();
  await page.getByRole("button", { name: "Save" }).click();

  // A no-op save round-trips through POST /templates/validate then
  // PUT /templates/{id} — success closes the SideSheet with no error banner.
  await expect(page.getByText("Edit default-implementation")).toHaveCount(0);
});
