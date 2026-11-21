import type { Locator, Page } from "@playwright/test";


// Builds a real PNG in the page (a canvas) and pastes it — the same
// ClipboardEvent a ⌘V of a screenshot produces, clipboard name and all.
export async function pasteScreenshot(target: Locator) {
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

export async function dropTextFile(page: Page, target: Locator, name: string, content = "line one\nline two\n", type = "text/plain") {
  const dt = await page.evaluateHandle(
    ([n, c, ty]) => {
      const dt = new DataTransfer();
      dt.items.add(new File([c], n, { type: ty }));
      return dt;
    },
    [name, content, type] as const,
  );
  await target.dispatchEvent("dragenter", { dataTransfer: dt });
  await target.dispatchEvent("dragover", { dataTransfer: dt });
  await target.dispatchEvent("drop", { dataTransfer: dt });
}
