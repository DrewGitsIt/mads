import { test, expect } from "./fixtures";
import type { Locator, Page } from "@playwright/test";
import { createSession } from "./lib/seed";

// Builds a real PNG in the page (a canvas) and pastes it — the same
// ClipboardEvent a ⌘V of a screenshot produces, clipboard name and all.
async function pasteScreenshot(target: Locator) {
  await target.evaluate(async (el) => {
    const canvas = document.createElement("canvas");
    canvas.width = 800;
    canvas.height = 500;
    const ctx = canvas.getContext("2d")!;
    ctx.fillStyle = "#c0392b";
    ctx.fillRect(0, 0, 800, 500);
    ctx.fillStyle = "#fff";
    ctx.font = "48px sans-serif";
    ctx.fillText("screenshot", 250, 260);
    const blob = await new Promise<Blob>((r) => canvas.toBlob((b) => r(b!), "image/png"));
    const dt = new DataTransfer();
    dt.items.add(new File([blob], "image.png", { type: "image/png" }));
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  });
}

async function dropTextFile(page: Page, target: Locator, name: string) {
  const dt = await page.evaluateHandle((n) => {
    const dt = new DataTransfer();
    dt.items.add(new File(["line one\nline two\n"], n, { type: "text/plain" }));
    return dt;
  }, name);
  await target.dispatchEvent("dragenter", { dataTransfer: dt });
  await target.dispatchEvent("dragover", { dataTransfer: dt });
  await target.dispatchEvent("drop", { dataTransfer: dt });
}

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
  await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
  await page.getByRole("button", { name: "Send" }).click();
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
  await page.getByRole("button", { name: "+ New session" }).click();

  await expect(page.getByLabel("Describe your goal")).toHaveValue("half-written goal");
  await expect(page.getByLabel(/^Remove screenshot-.*\.png$/)).toBeVisible();
});
