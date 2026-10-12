import { test, expect } from "./fixtures";

/**
 * A message the harness fails (expired login, API unreachable, rate limit)
 * reads as not delivered, offers Retry, and a successful retry leaves one
 * copy of the message — with the failed try folded into a count — rather
 * than the user re-pasting it and the history showing it twice. The fake's
 * "fail once <x>" fails its first send and echoes the second. The collapse is
 * derived from the transcript, so it survives a reload. (The session's name
 * avoids "retry": other specs look up a "Retry" button by loose name, and the
 * rail's row buttons carry the session name.)
 */
test("a failed send offers Retry and a successful retry reads as one message", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  const token = `${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(`echo resend probe ${token}`);
  await page.getByRole("button", { name: "Start session" }).click();
  await expect(page.getByText(`you said resend probe ${token}`)).toBeVisible();

  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  const text = `fail once hello ${token}`;
  await composer.fill(text);
  await composer.press("Enter");

  const stream = page.getByTestId("conversation-stream");
  await expect(stream.getByText("Not delivered")).toBeVisible();
  await stream.getByRole("button", { name: "Retry" }).click();

  await expect(stream.getByText(`you said hello ${token}`)).toBeVisible();
  await expect(stream.getByText(text, { exact: true })).toHaveCount(1);
  await expect(stream.getByRole("button", { name: "1 failed attempt" })).toBeVisible();
  await expect(stream.getByText("Not delivered")).toHaveCount(0);

  // Reload lands on the Launchpad; reopen the session from the rail.
  await page.reload();
  await page.locator('[aria-label="Session list"]').getByText(`echo resend probe ${token}`).click();
  await expect(stream.getByText(`you said hello ${token}`)).toBeVisible();
  await expect(stream.getByText(text, { exact: true })).toHaveCount(1);
  await expect(stream.getByRole("button", { name: "1 failed attempt" })).toBeVisible();
});
