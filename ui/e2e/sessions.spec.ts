import { test, expect } from "./fixtures";

test("sessions journey: create, rail update, open, send, back", async ({ page, routerURL }) => {
  await page.goto(routerURL);

  const goal = `e2e sessions journey ${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(goal);
  await page.getByRole("button", { name: "Start session" }).click();

  // Session name is derived from the goal's first line (deriveSessionName) — the
  // creation payload leaves workspace/provider pills unset, exercising the
  // server-side resolveSessionDefaults inheritance path (sessiondefaults.go:19-).
  const rail = page.locator('[aria-label="Session list"]');
  const sessionRow = rail.getByRole("button", { name: goal });
  await expect(sessionRow).toBeVisible();

  await sessionRow.click();

  // b56eef4 fixed a blank-pane bug on session open — assert it stays fixed.
  await expect(page.getByText("No conversation yet")).toBeVisible();
  await expect(page.getByPlaceholder("Message the agent — @ to mention, / for commands")).toBeVisible();

  // Terminal is now a real backend transport switch (POST /transport)
  // against the session's own provider conversation, not the Electron pty
  // bridge — assert the switch completes (real network round trip against
  // the fake provider) without asserting on xterm.js's own canvas/DOM
  // content, which isn't this journey's concern.
  await page.getByRole("button", { name: "Terminal" }).click();
  await expect(page.locator('[data-testid="terminal-view"]')).toBeVisible();
  await page.getByRole("button", { name: "Chat" }).click();

  // History survives the round trip through terminal mode — never a blank
  // pane on the way back.
  await expect(page.getByText("No conversation yet")).toBeVisible();

  const message = "hello from the sessions journey";
  await page.getByPlaceholder("Message the agent — @ to mention, / for commands").fill(message);
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText(message)).toBeVisible();

  await page.getByRole("button", { name: "Back to Launchpad" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();
});

test("sessions journey: agent replies to a composer message", async ({ page, routerURL }) => {
  await page.goto(routerURL);

  const goal = `e2e reply journey ${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(goal);
  await page.getByRole("button", { name: "Start session" }).click();

  const rail = page.locator('[aria-label="Session list"]');
  await rail.getByRole("button", { name: goal }).click();

  await expect(page.getByText("No conversation yet")).toBeVisible();

  const token = `${Date.now()}`;
  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  await composer.fill(`echo ${token}`);
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByText(`echo ${token}`)).toBeVisible();
  await expect(page.getByText(`you said ${token}`)).toBeVisible();
});

test("sessions journey: close, resume affordance, delete", async ({ page, routerURL }) => {
  await page.goto(routerURL);

  const goal = `e2e lifecycle journey ${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(goal);
  const [createResponse] = await Promise.all([
    page.waitForResponse((res) => res.request().method() === "POST" && res.url().includes("/sessions")),
    page.getByRole("button", { name: "Start session" }).click(),
  ]);
  const sessionId: string = (await createResponse.json()).session.id;
  // Deleting unmounts SessionWorkspace, but its SSE subscription can still
  // be live when the backend's own session.deleted event arrives, firing
  // one last refetchSession() against a session that's already gone — a
  // real, expected race (same class as fixtures.ts's documented Composer
  // 404 case), not a bug this journey should chase.
  page.allowStatus(sessionId);

  const rail = page.locator('[aria-label="Session list"]');
  // getByText resolves to the smallest matching element (the name span), so
  // this stays unambiguous even once the row also gains a "Resume" button —
  // rail.getByRole("button", { name: goal }) would match both.
  const sessionRow = rail.getByText(goal, { exact: true });
  const resumeAffordance = rail.getByLabel(`Resume ${goal}`);
  await sessionRow.click();

  // Close: live child terminates, session goes inactive, summary job runs.
  // The kebab's "Resume" menu item is a plain button (accessible name
  // "Resume"), unlike the rail's resume affordance whose aria-label
  // ("Resume <session name>") overrides its accessible name — so
  // getByRole("button", { name: "Resume", exact: true }) stays unambiguous.
  await page.getByLabel("Workspace actions").click();
  await page.getByRole("button", { name: "Close", exact: true }).click();

  await expect(resumeAffordance).toBeVisible();
  await page.getByLabel("Workspace actions").click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Close", exact: true })).toHaveCount(0);

  // Resume is reachable here (menu item present, wired to
  // routerClient.resumeSession — covered by SessionWorkspace.test.tsx and
  // the router-level TestResumeSession), but isn't clicked-to-success in
  // this journey: the "fake" provider's session history is in-process
  // memory only (providers/fake/fake.go), so a real terminated child can
  // never actually resume once its process is gone — TestResumeUnknownRef
  // and TestResumeSession's own comment (services/router/internal/api/
  // api_test.go) document this as deliberate, pre-existing behavior, not
  // something this journey should paper over. The menu is already open
  // from the assertions above, so Delete is clicked directly.
  await page.getByRole("button", { name: "Delete", exact: true }).click();

  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("event log survive");

  await dialog.getByText("Cancel", { exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(sessionRow).toBeVisible();

  await page.getByLabel("Workspace actions").click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete", exact: true }).click();

  await expect(page.getByLabel("Describe your goal")).toBeVisible();
  await expect(sessionRow).toHaveCount(0);
});
