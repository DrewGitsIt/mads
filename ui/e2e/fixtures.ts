import { test as base, expect, type Page } from "@playwright/test";
import { tokenFor } from "./lib/auth";

export interface ExpectableStatusPage extends Page {
  /**
   * Escape hatch for a journey step that intentionally triggers a 4xx/5xx
   * (e.g. an invalid-template save, or Composer's not-yet-proxied
   * sendMessage 404) — the blanket "no unexpected bad responses" tripwire
   * below ignores any response whose URL contains urlSubstring.
   */
  allowStatus(urlSubstring: string): void;
}

export const test = base.extend<{ routerURL: string; withToken: boolean; page: ExpectableStatusPage }>({
  routerURL: async ({}, use) => {
    await use(process.env.MADS_ROUTER_URL!);
  },
  /**
   * Whether the context sends the shared topology's install token on every
   * request (extraHTTPHeaders) — what Electron's main process adds below the
   * page (docs/LOCAL-FRONT-DOOR-DESIGN.md H7). `test.use({ withToken: false })`
   * for a journey without it.
   */
  withToken: [true, { option: true }],
  extraHTTPHeaders: async ({ routerURL, withToken }, use) => {
    await use(withToken ? { Authorization: `Bearer ${tokenFor(routerURL)}` } : {});
  },
  page: async ({ page }, use) => {
    const consoleErrors: string[] = [];
    const badResponses: string[] = [];
    const allowed: string[] = [];

    const extended = page as ExpectableStatusPage;
    extended.allowStatus = (urlSubstring: string) => {
      allowed.push(urlSubstring);
    };

    page.on("console", (msg) => {
      if (msg.type() !== "error") return;
      // Chromium logs a "Failed to load resource: ... 404" console error for
      // every non-2xx response; msg.text() doesn't include the URL, but
      // msg.location().url does — check both so allowStatus covers this too.
      const url = msg.location().url;
      if (allowed.some((s) => msg.text().includes(s) || url.includes(s))) return;
      consoleErrors.push(msg.text());
    });
    page.on("response", (res) => {
      if (res.status() >= 400 && !allowed.some((s) => res.url().includes(s))) {
        badResponses.push(`${res.status()} ${res.url()}`);
      }
    });

    await use(extended);

    expect(consoleErrors, "unexpected console errors during the journey").toEqual([]);
    expect(badResponses, "unexpected 4xx/5xx responses during the journey").toEqual([]);
  },
});

export { expect };
