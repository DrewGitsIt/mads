import { test, expect } from "./fixtures";

test("sessions journey: hero goal opens the conversation and gets a reply", async ({ page, routerURL }) => {
  await page.goto(routerURL);

  const token = `${Date.now()}`;
  const goal = `echo hero journey ${token}`;
  await page.getByLabel("Describe your goal").fill(goal);
  await page.getByRole("button", { name: "Start session" }).click();

  // No rail click — creating from the hero lands directly in the conversation,
  // with the goal itself sent as the session's first message.
  await expect(page.getByPlaceholder("Message the agent — @ to mention, / for commands")).toBeVisible();
  // The goal text also appears in the rail entry and the header's editable
  // name button — neither is a <p>, so scoping to the transcript's message
  // paragraphs is what keeps this unambiguous (goal is also the derived
  // session name).
  const transcript = page.locator("#workspace-panel-conversation p");
  await expect(transcript.filter({ hasText: goal })).toBeVisible();
  await expect(page.getByText(`you said hero journey ${token}`)).toBeVisible();

  const rail = page.locator('[aria-label="Session list"]');
  // getByText resolves to the smallest matching element (the name span) —
  // the row is itself a role="button" whose computed accessible name also
  // folds in its nested "⋯ actions" trigger, so a role query here would be
  // ambiguous (see the third test's identical note).
  await expect(rail.getByText(goal, { exact: true })).toBeVisible();

  // Terminal round trip still works; history (the goal message) survives it.
  await page.getByRole("button", { name: "Terminal" }).click();
  await expect(page.locator('[data-testid="terminal-view"]')).toBeVisible();
  await page.getByRole("button", { name: "Chat" }).click();
  await expect(transcript.filter({ hasText: goal })).toBeVisible();

  await page.getByRole("button", { name: "Back to Launchpad" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();
});

test("sessions journey: agent replies to a composer message", async ({ page, routerURL }) => {
  await page.goto(routerURL);

  const goal = `e2e reply journey ${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(goal);
  await page.getByRole("button", { name: "Start session" }).click();

  // Already open — creating from the hero auto-opens the conversation, and
  // the goal itself is in flight as the session's first message. Wait for it
  // to land before typing a follow-up, so the auto-send's own textarea-clear
  // doesn't race with (and wipe out) what we're about to type.
  await expect(page.getByPlaceholder("Message the agent — @ to mention, / for commands")).toBeVisible();
  // Scoped to the transcript's message paragraphs — the goal is also the
  // derived session name, so it appears again (unscoped) in the rail and header.
  await expect(page.locator("#workspace-panel-conversation p").filter({ hasText: goal })).toBeVisible();

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
  // Already open — creating from the hero auto-opens the conversation.
  await expect(page.getByLabel("Workspace actions")).toBeVisible();

  // Close: live child terminates, session goes inactive, summary job runs.
  // The kebab's "Resume" menu item is a plain button (accessible name
  // "Resume"), unlike the rail's resume affordance whose aria-label
  // ("Resume <session name>") overrides its accessible name — so
  // getByRole("button", { name: "Resume", exact: true }) stays unambiguous.
  await page.getByLabel("Workspace actions").click();

  // All three lifecycle actions are always visible in the kebab — while the
  // session is still active, Resume and Delete render disabled with an
  // inline reason instead of being hidden (discovery beats minimalism).
  // Scoped to the open menu's own list: other sessions elsewhere on the page
  // (created by e2e tests running in parallel against the same backend) also
  // render "Resume ..." rail affordances, which an unscoped substring/regex
  // query would also match.
  const kebabList = page.getByRole("list");
  await expect(page.getByRole("button", { name: "Close", exact: true })).toBeEnabled();
  const deleteWhileActive = kebabList.getByRole("button", { name: /Delete/ });
  await expect(deleteWhileActive).toBeDisabled();
  await expect(deleteWhileActive).toContainText("Close the session first");
  const resumeWhileActive = kebabList.getByRole("button", { name: /Resume/ });
  await expect(resumeWhileActive).toBeDisabled();
  await expect(resumeWhileActive).toContainText("Session is already active");

  await page.getByRole("button", { name: "Close", exact: true }).click();

  await expect(resumeAffordance).toBeVisible();
  await page.getByLabel("Workspace actions").click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  // Close is now disabled-with-reason rather than absent — same discovery-
  // over-minimalism rule applies once activity flips.
  const closeWhileInactive = kebabList.getByRole("button", { name: /Close/ });
  await expect(closeWhileInactive).toBeVisible();
  await expect(closeWhileInactive).toBeDisabled();
  await expect(closeWhileInactive).toContainText("Session is already inactive");

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

  // Delete-from-rail: back at the Launchpad (workspace not open), the same
  // shared confirm dialog is reachable from the rail row's "⋯" menu.
  await page.getByRole("button", { name: "Back to Launchpad" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();

  await rail.getByLabel(`${goal} actions`).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();

  const railDialog = page.getByRole("alertdialog");
  await expect(railDialog).toBeVisible();
  await expect(railDialog).toContainText("event log survive");
  await railDialog.getByRole("button", { name: "Delete", exact: true }).click();

  await expect(sessionRow).toHaveCount(0);
});
