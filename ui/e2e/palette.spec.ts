import { test, expect } from "./fixtures";
import { createSession } from "./lib/seed";

test("palette journey: jump to a seeded session by name", async ({ page, routerURL }) => {
  const session = await createSession(routerURL, {
    name: `e2e palette journey ${Date.now()}`,
    provider: "fake",
  });

  await page.goto(routerURL);

  // ⌘K now belongs to the sessions rail's omni search (#2b); the palette
  // answers to Control/Cmd-Shift-P.
  await page.keyboard.press("Control+Shift+P");
  const input = page.getByPlaceholder("Jump to a session, start a pipeline, run retro…");
  await expect(input).toBeVisible();

  await input.fill(session.name);
  await page.getByRole("button", { name: `Jump to · ${session.name}` }).click();

  // handleDispatch's jump-session case sets activeId="sessions" +
  // jumpToSessionId, which SessionsSurface turns into setSelectedId —
  // assert the conversation pane for this specific session, not just some
  // conversation pane.
  await expect(page.locator("main").getByRole("button", { name: session.name, exact: true })).toBeVisible();
  await expect(
    page.getByPlaceholder("Message the agent — @ to mention, / for commands"),
  ).toBeVisible();
});
