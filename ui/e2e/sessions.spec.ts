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

  await page.getByRole("button", { name: "Terminal" }).click();
  await expect(page.getByText("Terminal mode requires the Electron shell.")).toBeVisible();
  await page.getByRole("button", { name: "Chat" }).click();

  // Composer.handleSend fires `void routerClient.sendMessage(...)` unawaited
  // against POST /sessions/{id}/messages, which the Router doesn't implement
  // yet (HANDOFF.md §0 Finding B) — allowlist that 404 and assert only the
  // local optimistic echo, not a reply.
  page.allowStatus("/messages");
  const message = "hello from the sessions journey";
  await page.getByPlaceholder("Message the agent — @ to mention, / for commands").fill(message);
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText(message)).toBeVisible();

  await page.getByRole("button", { name: "Back to Launchpad" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();
});

test.fixme(
  "sessions journey: agent replies to a composer message",
  async () => {
    // Blocked on session messaging being proxied through the Router
    // (HANDOFF.md §0 Finding B) — POST /sessions/{id}/messages doesn't exist
    // on the Router yet, only on the per-session child process.
  },
);
