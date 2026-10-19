import { test, expect } from "./fixtures";
import { createSession, importTasks } from "./lib/seed";

const PLACEHOLDER = "Jump to a session, run a pipeline, run retro…";

test("palette journey: jump to a seeded session by name", async ({ page, routerURL }) => {
  const session = await createSession(routerURL, {
    name: `e2e palette journey ${Date.now()}`,
    provider: "fake",
  });

  await page.goto(routerURL);

  // ⌘K now belongs to the sessions rail's omni search (#2b); the palette
  // answers to Control/Cmd-Shift-P.
  await page.keyboard.press("Control+Shift+P");
  const input = page.getByPlaceholder(PLACEHOLDER);
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

test("palette journey: Run pipeline… opens the session's Tasks tab in Plan and starts nothing", async ({
  page,
  routerURL,
}) => {
  const session = await createSession(routerURL, {
    name: `e2e palette run journey ${Date.now()}`,
    provider: "fake",
  });
  await importTasks(routerURL, session.id, ["e2e palette plan task"]);

  await page.goto(routerURL);
  await page.keyboard.press("Control+Shift+P");
  const input = page.getByPlaceholder(PLACEHOLDER);
  await input.fill(session.name);
  await page.getByRole("button", { name: `Run pipeline… · ${session.name}` }).click();

  await expect(page.locator("main").getByRole("button", { name: session.name, exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Tasks", exact: true })).toHaveAttribute("aria-selected", "true");
  const panel = page.locator("#workspace-panel-tasks");
  await expect(panel.locator('button[aria-haspopup="menu"]').first()).toHaveText(/New run/);
  await expect(panel.getByRole("button", { name: "Run 1 task" })).toBeEnabled();

  // Plan only: nothing was started for the session.
  const res = await fetch(`${routerURL}/events?session_id=${session.id}&types=pipeline.started`);
  expect(res.ok).toBeTruthy();
  expect(await res.json()).toEqual([]);
});
