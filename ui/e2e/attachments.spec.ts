import { test, expect } from "./fixtures";
import { createSession } from "./lib/seed";
import { dropTextFile, pasteScreenshot } from "./lib/attachments";

test("attachments journey: pasted and dropped files reach the agent as paths and show as thumbnails", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  const goalBox = page.getByLabel("Describe your goal");
  const token = `${Date.now()}`;
  await goalBox.fill(`echo look ${token}`);

  await pasteScreenshot(goalBox);
  await dropTextFile(page, goalBox, "build.log");

  const shotChip = page.getByLabel(/^Remove screenshot-.*\.png$/);
  await expect(shotChip).toBeVisible();
  await expect(page.getByLabel("Remove build.log")).toBeVisible();
  const chipThumb = shotChip.locator("xpath=..").locator("img");
  await expect(chipThumb).toBeVisible();
  await expect.poll(() => chipThumb.evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(320);

  await page.getByRole("button", { name: "Start session" }).click();
  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  await expect(composer).toBeVisible();

  // The opening message: typed text, a thumbnail and a file tile — no path list.
  const panel = page.locator("#workspace-panel-conversation");
  await expect(panel.getByText(`echo look ${token}`)).toBeVisible();
  const bubbleThumb = panel.getByAltText(/^screenshot-.*\.png$/);
  await expect(bubbleThumb).toBeVisible();
  await expect.poll(() => bubbleThumb.evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(320);
  await expect(panel.getByRole("link", { name: /build\.log/ })).toBeVisible();
  await expect(panel.locator("[data-user-msg]").getByText(/Attached files/)).toHaveCount(0);
  // The agent got the paths (the fake echoes everything after "echo ").
  await expect(panel.getByText(new RegExp(`you said look ${token}`))).toBeVisible();

  // Live composer: drop a file, send it alone.
  await dropTextFile(page, composer, "trace.txt");
  await expect(page.getByLabel("Remove trace.txt")).toBeVisible();
  // Send while idle, Queue while a turn is still finishing (the button reads
  // which) — either way the file goes out.
  const send = page.getByRole("button", { name: /^(Send|Queue)$/ });
  await expect(send).toBeEnabled();
  await send.click();
  await expect(page.getByLabel("Remove trace.txt")).toHaveCount(0);
  await expect(panel.getByRole("link", { name: /trace\.txt/ })).toBeVisible();

  // Survives a refetch: reload and the thumbnails come back from the transcript.
  await page.reload();
  await page.locator('[aria-label="Session list"]').getByText(`echo look ${token}`, { exact: true }).click();
  await expect(page.locator("#workspace-panel-conversation").getByAltText(/^screenshot-.*\.png$/)).toBeVisible();
  await expect(page.locator("#workspace-panel-conversation").getByRole("link", { name: /trace\.txt/ })).toBeVisible();
});

test("attachments journey: an unsent hero goal and its files survive visiting another session", async ({
  page,
  routerURL,
}) => {
  const other = await createSession(routerURL, { name: `e2e draft detour ${Date.now()}`, provider: "fake" });
  await page.goto(routerURL);
  const goalBox = page.getByLabel("Describe your goal");
  await goalBox.fill("half-written goal");
  await pasteScreenshot(goalBox);
  await expect(page.getByLabel(/^Remove screenshot-.*\.png$/)).toBeVisible();

  await page.locator('[aria-label="Session list"]').getByText(other.name, { exact: true }).click();
  await expect(page.getByPlaceholder("Message the agent — @ to mention, / for commands")).toBeVisible();
  await page.getByRole("button", { name: "New session" }).click();

  await expect(page.getByLabel("Describe your goal")).toHaveValue("half-written goal");
  await expect(page.getByLabel(/^Remove screenshot-.*\.png$/)).toBeVisible();
});

// An attachment's bytes are whoever attached them (a paired device at chat
// level can), and its tile opens them on this Router's origin, where the
// app's credentials are: an HTML (or SVG) file downloads instead of
// rendering, so its script never runs here. Text still opens as text.
test("attachments journey: an attached HTML file downloads, it never renders as a page", async ({ page, routerURL }) => {
  await page.goto(routerURL);
  const goalBox = page.getByLabel("Describe your goal");
  const token = `${Date.now()}`;
  await goalBox.fill(`echo page ${token}`);
  await page.getByRole("button", { name: "Start session" }).click();
  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  await expect(composer).toBeVisible();
  const panel = page.locator("#workspace-panel-conversation");
  await expect(panel.getByText(new RegExp(`you said page ${token}`))).toBeVisible();

  await dropTextFile(page, composer, "evil.html", `<html><title>ran</title><script>document.title = "script ran"</script></html>`, "text/html");
  await dropTextFile(page, composer, "notes.txt", "plain words\n");
  await expect(page.getByLabel("Remove evil.html")).toBeVisible();
  await expect(page.getByLabel("Remove notes.txt")).toBeVisible();
  const send = page.getByRole("button", { name: /^(Send|Queue)$/ });
  await expect(send).toBeEnabled();
  await send.click();
  const htmlTile = panel.getByRole("link", { name: /evil\.html/ });
  const textTile = panel.getByRole("link", { name: /notes\.txt/ });
  await expect(htmlTile).toBeVisible();
  await expect(textTile).toBeVisible();
  const htmlURL = new URL((await htmlTile.getAttribute("href"))!, routerURL).toString();
  const textURL = new URL((await textTile.getAttribute("href"))!, routerURL).toString();

  // Opened, the HTML file is a download: no page, no script.
  const viewer = await page.context().newPage();
  const [download] = await Promise.all([viewer.waitForEvent("download"), viewer.goto(htmlURL).catch(() => null)]);
  expect(download.suggestedFilename()).toBe("evil.html");
  expect(await viewer.title()).not.toBe("script ran");
  // The text file still opens inline, as text.
  const res = await viewer.goto(textURL);
  expect(res?.headers()["content-type"]).toMatch(/^text\/plain/);
  expect(res?.headers()["content-security-policy"]).toContain("sandbox");
  await expect(viewer.locator("body")).toContainText("plain words");
  await viewer.close();
});
