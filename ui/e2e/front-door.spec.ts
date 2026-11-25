import { test, expect } from "./fixtures";
import { tokenFor } from "./lib/auth";

// The local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md H7): every Router API
// call carries `Authorization: Bearer <install token>`, added below the page
// — by Electron's main process for the app, by the context's
// extraHTTPHeaders here. There is no cookie and no sign-in flow.

test("with the token below the page, the app loads and its EventSource carries the header", async ({ page, routerURL }) => {
  const sse: Record<string, string>[] = [];
  page.on("request", (req) => {
    if (new URL(req.url()).pathname === "/events") sse.push(req.headers());
  });
  await page.goto(routerURL);
  await expect(page.getByLabel("Settings")).toBeVisible();
  await expect.poll(() => sse.length).toBeGreaterThan(0);
  expect(sse[0].authorization).toBe(`Bearer ${tokenFor(routerURL)}`);
  // Nothing token-shaped is left for page code: no cookie.
  expect(await page.evaluate(() => document.cookie)).toBe("");
  expect(await page.context().cookies(routerURL)).toEqual([]);
});

test.describe("without the token", () => {
  test.describe.configure({ mode: "serial" });
  test.use({ withToken: false });

  test("the app says mads can't reach its token, truthfully", async ({ page, routerURL }) => {
    // Every API call is refused; that is the journey.
    page.allowStatus(new URL(routerURL).host);
    page.allowStatus("no valid install token");
    await page.goto(routerURL);
    await expect(page.getByText("mads can't reach its token")).toBeVisible();
    await expect(page.getByText(/Restart the app/)).toBeVisible();
    await expect(page.getByText(/Can.t reach the mads backend/)).toHaveCount(0);
  });

  test("the API refuses a caller without the token", async ({ routerURL }) => {
    const res = await fetch(`${routerURL}/sessions`);
    expect(res.status).toBe(401);
    expect(((await res.json()) as { code: string }).code).toBe("unauthorized");
    const health = (await (await fetch(`${routerURL}/health`)).json()) as Record<string, string>;
    expect(Object.keys(health).sort()).toEqual(["service", "status"]);
    // The sign-in routes are gone (H7).
    for (const [m, p] of [
      ["POST", "/auth/code"],
      ["GET", "/auth/enter?code=x"],
    ]) {
      const r = await fetch(`${routerURL}${p}`, { method: m, headers: { Authorization: `Bearer ${tokenFor(routerURL)}` } });
      expect(r.status, `${m} ${p}`).not.toBe(200);
      expect(r.headers.get("set-cookie"), `${m} ${p}`).toBeNull();
    }
  });
});
