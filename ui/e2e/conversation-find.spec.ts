import { test, expect } from "./fixtures";

/**
 * ⌘F / Ctrl+F finds text in the conversation: the count, stepping with
 * Enter / Shift+Enter, match case, the CSS highlights the matches are
 * painted with, and Esc clearing them. (Matches inside collapsed activity
 * are covered by ChatRendererFind.test.tsx — the fake provider makes no tool
 * calls.)
 */
test("find in conversation counts, steps through and highlights matches", async ({ page, routerURL }) => {
  await page.goto(routerURL);
  const token = `${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(`echo find probe alpha ${token}`);
  await page.getByRole("button", { name: "Start session" }).click();
  const stream = page.getByTestId("conversation-stream");
  await expect(stream.getByText(`you said find probe alpha ${token}`)).toBeVisible();

  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
  await composer.fill(`echo alpha beta ${token}`);
  await composer.press("Enter");
  await expect(stream.getByText(`you said alpha beta ${token}`)).toBeVisible();

  // The Router runs here, so its platform is this one.
  const mod = process.platform === "darwin" ? "Meta" : "Control";
  await composer.press(`${mod}+f`);
  const input = page.getByLabel("Find in conversation");
  await expect(input).toBeFocused();

  await input.fill("alpha");
  const bar = page.getByRole("search");
  await expect(bar.getByText("1 of 4")).toBeVisible();
  const painted = () =>
    page.evaluate(() => ({
      matches: CSS.highlights.get("find-match")?.size ?? 0,
      current: CSS.highlights.get("find-current")?.size ?? 0,
    }));
  await expect.poll(painted).toEqual({ matches: 3, current: 1 });

  await input.press("Enter");
  await expect(bar.getByText("2 of 4")).toBeVisible();
  await input.press("Shift+Enter");
  await input.press("Shift+Enter");
  await expect(bar.getByText("4 of 4")).toBeVisible();

  await input.fill("ALPHA");
  await expect(bar.getByText(/of 4$/)).toBeVisible();
  await bar.getByRole("button", { name: "Match case" }).click();
  await expect(bar.getByText("No results")).toBeVisible();
  await expect.poll(painted).toEqual({ matches: 0, current: 0 });

  await input.press("Escape");
  await expect(bar).toHaveCount(0);
  await expect(composer).toBeFocused();
});
