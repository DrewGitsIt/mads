import { test, expect } from "./fixtures";

/**
 * The conversation pane must never grow a horizontal scrollbar: message rows
 * are flex items, and without min-width:0 + overflow-wrap on the prose column
 * a single unbreakable token (a long path, an inline-code run) sets the row's
 * min-content width to the token's full length — the pane scrolls sideways
 * into dead whitespace right of the messages (regression seen live
 * 2026-09-09). Fenced code blocks instead scroll inside their own pre.
 */

/** Elements inside the conversation panel whose content is wider than their box. */
async function horizontalOverflowers(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const panel = document.querySelector("#workspace-panel-conversation");
    if (!panel) return ["<no conversation panel>"];
    return ([panel, ...panel.querySelectorAll("*")] as HTMLElement[])
      .filter((el) => el.scrollWidth > el.clientWidth + 1)
      // A pre or a table is allowed to scroll horizontally — that's where
      // wide code blocks and tables are supposed to overflow.
      .filter((el) => el.tagName !== "PRE" && el.tagName !== "TABLE")
      .map((el) => `${el.tagName}.${el.getAttribute("class") ?? ""}`);
  });
}

test("conversation pane never scrolls horizontally on unbreakable content", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);

  const token = `${Date.now()}`;
  await page.getByLabel("Describe your goal").fill(`echo overflow probe ${token}`);
  await page.getByRole("button", { name: "Start session" }).click();
  await expect(page.getByText(`you said overflow probe ${token}`)).toBeVisible();
  const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");

  // One long unbreakable plain token, one as inline code (markdown <code>).
  const longWord = "A".repeat(400);
  await composer.fill(`echo ${longWord}`);
  await composer.press("Enter");
  await expect(page.getByText(`you said ${longWord}`)).toBeVisible();

  await composer.fill(`echo \`${"B".repeat(400)}\``);
  await composer.press("Enter");
  // Two matches, not one: your own message row AND the agent's echo of it
  // both carry the token. toBeVisible() raced that — locally it resolved
  // while only the first had rendered and passed; on CI both landed first
  // and strict mode rejected the ambiguous locator. Asserting the count
  // waits for the settled state, which is also exactly what the overflow
  // measurement below needs rendered.
  await expect(page.getByText("B".repeat(400))).toHaveCount(2);

  // A wide fenced code block and a wide table. The code block sits in a
  // Fold (a grid): an auto column there grew to the pre's full width once
  // the fold settled, widening the whole stream (seen live 2026-10-12).
  const wide = "x = " + "call(".repeat(200) + ";";
  const cells = (s: string) => `| ${Array.from({ length: 14 }, (_, i) => `${s}_${i}`).join(" | ")} |`;
  const table = `${cells("column_header")}\n|${" --- |".repeat(14)}\n${cells("value")}`;
  await composer.fill(`echo wide\n\n\`\`\`\n${wide}\n\`\`\`\n\n${table}\n\nwide-end ${token}`);
  await composer.press("Enter");
  await expect(page.getByText(`wide-end ${token}`)).toHaveCount(2);
  // Past the fold's 240ms settle, when the inner box stops clipping.
  await page.waitForTimeout(400);

  expect(await horizontalOverflowers(page)).toEqual([]);
});
