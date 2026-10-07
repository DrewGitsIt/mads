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
  // Into terminal from the composer's + menu (View), back through the dock's
  // leading Chat segment.
  await page.getByRole("button", { name: "Input options" }).click();
  await page.getByRole("group", { name: "View" }).getByRole("button", { name: /Terminal/ }).click();
  await expect(page.locator('[data-testid="terminal-view"]')).toBeVisible();
  const dock = page.locator('[data-testid="session-dock"]');
  await expect(dock).toHaveAttribute("data-mode", "terminal");
  await dock.getByRole("button", { name: "Chat", exact: true }).click();
  await expect(transcript.filter({ hasText: goal })).toBeVisible();
  await expect(dock).toHaveAttribute("data-mode", "chat");

  // "+ New session" clears the open workspace back to the Launchpad (the
  // dedicated "Back to Launchpad" button no longer exists).
  await page.getByRole("button", { name: "+ New session" }).click();
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
  // Queue if the goal's own turn is still finishing — it goes out right after.
  await page.getByRole("button", { name: /^(Send|Queue)$/ }).click();

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
  const sessionCard = sessionRow.locator('xpath=ancestor::div[@role="button"][1]');
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
  // session is still active, Resume renders disabled with an inline reason
  // instead of being hidden (discovery beats minimalism).
  // Scoped to the open menu's own list: other sessions elsewhere on the page
  // (created by e2e tests running in parallel against the same backend) also
  // render "Resume ..." rail affordances, which an unscoped substring/regex
  // query would also match.
  const kebabList = page.getByRole("list");
  await expect(page.getByRole("button", { name: "Close", exact: true })).toBeEnabled();
  // Delete is NOT disabled here any more: the router terminates the session as
  // part of the delete, so the warning belongs in the confirm dialog rather
  // than in a dead menu item that pre-empts it.
  const deleteWhileActive = kebabList.getByRole("button", { name: /Delete/ });
  await expect(deleteWhileActive).toBeEnabled();
  const resumeWhileActive = kebabList.getByRole("button", { name: /Resume/ });
  await expect(resumeWhileActive).toBeDisabled();
  await expect(resumeWhileActive).toContainText("Session is already active");

  // The live session's dialog names it and carries the verb the delete will
  // actually perform. Whether the first turn is still in flight decides
  // between "Stop and delete" and "Close and delete" — both are the point,
  // which is that a live session's button no longer reads a bare "Delete".
  await deleteWhileActive.click();
  const liveDialog = page.getByRole("alertdialog");
  await expect(liveDialog).toContainText(`Delete “${goal}”?`);
  await expect(
    liveDialog.getByRole("button", { name: /^(Close|Stop) and delete$/ }),
  ).toBeVisible();
  await liveDialog.getByText("Cancel", { exact: true }).click();
  await expect(liveDialog).toHaveCount(0);

  await page.getByLabel("Workspace actions").click();
  await page.getByRole("button", { name: "Close", exact: true }).click();

  await expect(resumeAffordance).toBeVisible();
  // Rail repaints live, no reload. The Active/Recent sections are gone (one
  // flat list, mockup §2 option B), so the card's own status line is the
  // signal — and unlike a group header it is scoped to THIS row, which other
  // e2e tests running in parallel against the same backend cannot disturb.
  await expect(sessionCard.getByRole("status")).toContainText("Sleeping");
  await page.getByLabel("Workspace actions").click();
  await expect(page.getByRole("button", { name: "Resume", exact: true })).toBeVisible();
  // Close stays enabled even while inactive ("honest Close", 495afa4): the
  // activity record can drift from the real child state, so Close is always
  // offered and the server treats an already-inactive close as a no-op.
  const closeWhileInactive = kebabList.getByRole("button", { name: /Close/ });
  await expect(closeWhileInactive).toBeVisible();
  await expect(closeWhileInactive).toBeEnabled();

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
  await expect(dialog).toContainText(`Delete “${goal}”?`);
  await expect(dialog).toContainText("Sleeping");
  await expect(dialog).toContainText("event log survive");

  await dialog.getByText("Cancel", { exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(sessionRow).toBeVisible();

  // Delete-from-rail: back at the Launchpad ("+ New session" clears the open
  // workspace), the same shared confirm dialog is reachable from the rail
  // row's 🗑 button (aria-label "Archive <name>").
  await page.getByRole("button", { name: "+ New session" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();

  // The action cluster hangs outside the card and is inert until the row is
  // hovered (or focused) — that hover is what gives it its position:fixed
  // coordinates, and until it has them the stylesheet keeps it transparent
  // and pointer-events:none. Clicking it therefore has to start the way a
  // real pointer does: on the card. Without this, Playwright's actionability
  // check never moves the mouse, so the cluster never anchors and the click
  // hit-tests straight through to the card's own status line.
  await sessionCard.hover();
  await rail.getByLabel(`Archive ${goal}`).click();

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
  // The rail is one flat list now, so "went inactive" is read off this card's
  // own status line rather than which group header it sits under.
  const sessionStatus = sessionRow.locator('xpath=ancestor::div[@role="button"][1]').getByRole("status");
  await expect(sessionStatus).toContainText("Sleeping");

  // ...then release batch A (earlier-issued, stale) after. Without the
  // request-ordering guard in useSessionsList.ts, this overwrites the rail
  // back to showing the session active — reproducing the reported bug.
  for (let i = 0; i < batchABoundary; i++) await held[i].fulfill();
  await expect(sessionStatus).toContainText("Sleeping");
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
    page.getByRole("button", { name: /^(Send|Queue)$/ }).click(),
  ]);
  expect(sendResponse.status()).toBe(202);

  await expect(page.getByText(`echo ${token}`)).toBeVisible();
  await expect(page.getByText(`you said ${token}`)).toBeVisible();
});
