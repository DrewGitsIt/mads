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

  const message = "hello from the sessions journey";
  await page.getByPlaceholder("Message the agent — @ to mention, / for commands").fill(message);
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText(message)).toBeVisible();

  await page.getByRole("button", { name: "Back to Launchpad" }).click();
  await expect(page.getByLabel("Describe your goal")).toBeVisible();
});

/**
 * DESCOPE DECISION (executor, this task): stays unimplemented, for a
 * different and deeper reason than it looked at first.
 *
 * This task did add the Router's missing POST /sessions/{id}/messages and
 * /interrupt proxy routes (see stream_forward.go, route_manifest_test.go) —
 * that part of the original gap is fixed and verified: sending a message no
 * longer 404s (the "create, rail update, open, send, back" journey above no
 * longer needs an allowStatus escape hatch for it).
 *
 * But the reply never becomes observable through the UI, and the root cause
 * is upstream of the UI entirely: GET /sessions/{id}/transcript
 * (transcriptFor, transcript.go) reads via `s.providers.Get(sess.Provider)`
 * — the *Router process's own* provider.Registry — while the actual turn
 * runs inside the spawned session child's separate process, against *its
 * own* separate provider instance. Verified by hand against the real
 * topology (data+router+session binaries, provider=fake): POST /messages
 * returns 202 and the child's log shows nothing wrong, but every subsequent
 * GET /transcript comes back `[]`, and the router log repeats
 * `api: transcript: read session <id>: unknown session ref "fake-1"` —
 * transcriptFor's error path degrades that to an empty array by design
 * (transcript.go's doc comment), which is why it doesn't 5xx or trip the
 * no-bad-response fixture; it just silently never shows the reply. This
 * isn't fake-provider-specific plumbing either: providers/claudecode
 * doesn't implement TranscriptReader at all, so no provider transcript
 * currently reflects a live interactive session's turns across the
 * router/session process boundary — a live conversation's transcript is
 * only ever readable within the process that ran the turn.
 *
 * Fixing this for real means making transcriptFor proxy to the running
 * child (mirroring /output, /messages, /interrupt's addr-lookup pattern)
 * when the session is live, falling back to the provider registry only for
 * sessions with no running child — a real behavior change to a
 * general-purpose read endpoint, not a route-wiring gap. Out of proportion
 * for this task; flagging as a genuine, previously-undiscovered product bug
 * that likely also affects real (non-fake) interactive sessions once a
 * provider does implement TranscriptReader. Needs its own task.
 */
test.fixme(
  "sessions journey: agent replies to a composer message",
  async () => {
    // Blocked on GET /sessions/{id}/transcript not reflecting a live
    // session child's conversation state across the router/session process
    // boundary — see the DESCOPE DECISION comment above.
  },
);
