import { test, expect } from "./fixtures";
import { createSessionNoSpawn } from "./lib/seed";

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
  // "echo slowly" paces the fake provider's reply word-by-word (250ms/chunk,
  // providers/fake/fake.go) so the working indicator has an observable
  // in-flight window — no second send anywhere in this arc.
  await composer.fill(`echo slowly ${token}`);
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByText(`echo slowly ${token}`)).toBeVisible();
  await expect(page.getByRole("status", { name: /Working…|Using / })).toBeVisible();
  await expect(page.getByText(`you said ${token}`)).toBeVisible();
  await expect(page.getByRole("status", { name: /Working…|Using / })).not.toBeVisible();
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
  // Rail regroups live, no reload: "Active"/"Recent" are two stacked
  // sections with no DOM hook beyond their header text (see
  // railGroups.ts/SessionRail.tsx), and other e2e tests running in parallel
  // against the same backend may keep an Active section on-screen too — so
  // proving this row left Active means checking its position against the
  // Recent header, not just that the header exists.
  const recentHeader = rail.getByText("Recent", { exact: true });
  await expect(recentHeader).toBeVisible();
  await expect(async () => {
    const rowBox = await sessionRow.boundingBox();
    const recentHeaderBox = await recentHeader.boundingBox();
    expect(rowBox).not.toBeNull();
    expect(recentHeaderBox).not.toBeNull();
    expect(rowBox!.y).toBeGreaterThan(recentHeaderBox!.y);
  }).toPass();
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

/**
 * Polls getCount() until it has reached at least minCount and hasn't
 * changed for quietMs — used to wait for a whole *batch* of GET /sessions
 * requests triggered by one SSE event, since two independent
 * useSessionsList() instances are mounted at once (SessionsSurface's rail
 * and App.tsx's always-mounted useCommandPaletteData), so one event fires
 * more than one refetch and the exact count isn't a stable assumption to
 * hard-code.
 */
async function waitForRequestBatch(
  getCount: () => number,
  minCount: number,
  quietMs = 300,
  timeoutMs = 10_000,
): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  let last = -1;
  let lastChange = Date.now();
  while (Date.now() < deadline) {
    const current = getCount();
    if (current !== last) {
      last = current;
      lastChange = Date.now();
    } else if (current >= minCount && Date.now() - lastChange >= quietMs) {
      return current;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`waitForRequestBatch: never reached ${minCount} and stabilized (last=${last})`);
}

test("sessions journey: close survives an out-of-order rail refetch", async ({ page, routerURL }) => {
  // Reproduces the actual stale-rail defect: useSessionsList.refetch() fires
  // on every session.* SSE event with no request-ordering guard
  // (useSessionsList.ts). An unrelated refetch already in flight when the
  // close's own refetch is issued can resolve *after* it and overwrite the
  // freshly-correct "inactive" state with the stale snapshot it captured —
  // and since no further event arrives to trigger another refetch, the rail
  // is stuck exactly like the bug report ("stays stale... until the whole
  // UI is restarted"). This test forces that resolution order deterministically
  // instead of hoping to catch it on timing luck.
  const dataURL = process.env.MADS_DATA_URL!;

  await page.goto(routerURL);

  const goal = `e2e race journey ${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(goal);
  const [createResponse] = await Promise.all([
    page.waitForResponse((res) => res.request().method() === "POST" && res.url().includes("/sessions")),
    page.getByRole("button", { name: "Start session" }).click(),
  ]);
  const sessionId: string = (await createResponse.json()).session.id;
  // Closing terminates the child; ConversationTab's still-mounted live-output
  // SSE subscription then 404s against the now-childless session — same
  // expected race as the "close, resume affordance, delete" journey's own
  // allowStatus(sessionId) call.
  page.allowStatus(sessionId);
  await expect(page.getByPlaceholder("Message the agent — @ to mention, / for commands")).toBeVisible();

  const rail = page.locator('[aria-label="Session list"]');
  const sessionRow = rail.getByText(goal, { exact: true });
  await expect(sessionRow).toBeVisible();

  // Hold every GET /sessions response instead of letting it land — captured
  // in issue order, released under this test's control below.
  const held: { fulfill: () => Promise<void> }[] = [];
  await page.route("**/sessions", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const response = await route.fetch();
    held.push({ fulfill: () => route.fulfill({ response }) });
  });

  // Batch A: an unrelated session.created event (a second, never-opened
  // session) triggers a refetch in every mounted useSessionsList instance
  // while our session is still active — these GETs are captured (and held)
  // with that still-active snapshot.
  await createSessionNoSpawn(dataURL, { name: `unrelated ${Date.now()}`, provider: "fake" });
  const batchABoundary = await waitForRequestBatch(() => held.length, 1);

  // Batch B: close the real session — its refetch(es) are issued (and
  // held) strictly after batch A's, capturing the correct post-close
  // "inactive" snapshot.
  await page.getByLabel("Workspace actions").click();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  const batchBBoundary = await waitForRequestBatch(() => held.length, batchABoundary + 1);

  // Release batch B (later-issued, correct) first...
  for (let i = batchABoundary; i < batchBBoundary; i++) await held[i].fulfill();
  const recentHeader = rail.getByText("Recent", { exact: true });
  await expect(recentHeader).toBeVisible();
  await expect(async () => {
    const rowBox = await sessionRow.boundingBox();
    const recentHeaderBox = await recentHeader.boundingBox();
    expect(rowBox).not.toBeNull();
    expect(recentHeaderBox).not.toBeNull();
    expect(rowBox!.y).toBeGreaterThan(recentHeaderBox!.y);
  }).toPass();

  // ...then release batch A (earlier-issued, stale) after. Without the
  // request-ordering guard in useSessionsList.ts, this overwrites the rail
  // back to showing the session active — reproducing the reported bug.
  for (let i = 0; i < batchABoundary; i++) await held[i].fulfill();
  await expect(async () => {
    const rowBox = await sessionRow.boundingBox();
    const recentHeaderBox = await recentHeader.boundingBox();
    expect(rowBox).not.toBeNull();
    expect(recentHeaderBox).not.toBeNull();
    expect(rowBox!.y).toBeGreaterThan(recentHeaderBox!.y);
  }).toPass();
});

test("sessions journey: sending a message wakes a dormant session", async ({ page, routerURL }) => {
  const dataURL = process.env.MADS_DATA_URL!;
  const name = `e2e wake journey ${Date.now()}`;
  const session = await createSessionNoSpawn(dataURL, { name, provider: "fake" });
  // ConversationTab opens its live-output SSE subscription (GET /output) on
  // mount regardless of activity, independent of the composer's own
  // wake-on-send path — for a session with no child yet, that first
  // connection attempt 404s until the send below wakes it. Same class of
  // expected race as the "close, resume affordance, delete" journey's own
  // allowStatus(sessionId) call.
  page.allowStatus(session.id);

  await page.goto(routerURL);
  const rail = page.locator('[aria-label="Session list"]');
  await rail.getByText(name, { exact: true }).click();

  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  await expect(composer).toBeVisible();
  const token = `${Date.now()}`;
  await composer.fill(`echo ${token}`);

  const [sendResponse] = await Promise.all([
    page.waitForResponse(
      (res) => res.request().method() === "POST" && res.url().includes(`/sessions/${session.id}/messages`),
    ),
    page.getByRole("button", { name: "Send" }).click(),
  ]);
  expect(sendResponse.status()).toBe(202);

  await expect(page.getByText(`echo ${token}`)).toBeVisible();
  await expect(page.getByText(`you said ${token}`)).toBeVisible();
});
