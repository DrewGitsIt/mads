import { createServer, type Server } from "node:http";
import { test, expect } from "./fixtures";
import { apiFetch } from "./lib/auth";

// Adversarial checks on the local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md
// D3, H7): a page on *another localhost port* is same-site with the Router.
// Here its requests even carry the token (Playwright's extraHTTPHeaders go
// to every origin; Electron's hook is scoped to the Router's), so what has to
// stop such a page is localguard (Origin / Sec-Fetch-Site).

async function sessionCount(routerURL: string): Promise<number> {
  const res = await apiFetch(`${routerURL}/sessions`);
  expect(res.status).toBe(200);
  return ((await res.json()) as unknown[]).length;
}

test.describe("a page on another localhost port", () => {
  test.describe.configure({ mode: "serial" });
  let other: Server;
  let otherURL = "";

  test.beforeAll(async () => {
    other = createServer((_req, res) => {
      res.setHeader("Content-Type", "text/html");
      res.end("<!doctype html><title>other</title><body>other app</body>");
    });
    await new Promise<void>((r) => other.listen(0, "127.0.0.1", r));
    otherURL = "";
  });
  test.afterAll(async () => {
    await new Promise<void>((r) => other.close(() => r()));
  });

  test("can't drive the Router even with the token attached", async ({ page, routerURL }) => {
    const router = new URL(routerURL);
    const port = (other.address() as { port: number }).port;
    // Same host name as the Router, so the browser treats it as same-site.
    otherURL = `${router.protocol}//${router.hostname}:${port}/`;
    page.allowStatus(router.host);
    page.allowStatus("Failed to fetch");
    page.allowStatus("CORS");
    page.allowStatus("Access-Control");
    page.allowStatus("net::ERR");


    const before = await sessionCount(routerURL);
    await page.goto(otherURL);
    const results = await page.evaluate(async (r) => {
      const out: Record<string, string> = {};
      // A CORS "simple" request (no preflight): text/plain JSON body.
      try {
        const res = await fetch(`${r}/sessions`, {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "text/plain" },
          body: JSON.stringify({ name: "from-another-port", provider: "fake" }),
        });
        out.simplePost = String(res.status);
      } catch (e) {
        out.simplePost = `threw ${String(e)}`;
      }
      // no-cors: an opaque response, but the side effect would still happen.
      try {
        await fetch(`${r}/sessions`, {
          method: "POST",
          mode: "no-cors",
          credentials: "include",
          headers: { "Content-Type": "text/plain" },
          body: JSON.stringify({ name: "from-another-port-nocors", provider: "fake" }),
        });
        out.noCors = "sent";
      } catch (e) {
        out.noCors = `threw ${String(e)}`;
      }
      // A navigation-free image probe of an API route.
      out.img = await new Promise<string>((done) => {
        const img = new Image();
        img.onload = () => done("loaded");
        img.onerror = () => done("error");
        img.src = `${r}/sessions?probe=1`;
      });
      return out;
    }, routerURL.replace(/\/$/, ""));

    // A top-level form POST: Sec-Fetch-Site same-site, no CORS at all.
    await page.setContent(
      `<form id="f" method="POST" action="${routerURL.replace(/\/$/, "")}/sessions" enctype="text/plain">` +
        `<input name='{"name":"from-form","provider":"fake","x":"' value='"}'></form>`,
    );
    const nav = page.waitForNavigation().catch(() => undefined);
    await page.evaluate(() => (document.getElementById("f") as HTMLFormElement).submit());
    const resp = await nav;
    const formStatus = resp ? resp.status() : 0;

    const after = await sessionCount(routerURL);
    expect(after, `sessions created from another port: ${JSON.stringify(results)} form=${formStatus}`).toBe(before);
    expect(formStatus === 0 || formStatus === 403 || formStatus === 401, `form POST status ${formStatus}`).toBe(true);
  });
});

test("the app's EventSource and same-origin fetches work with the header added below the page", async ({ page, routerURL }) => {
  await page.goto(routerURL);
  await expect(page.getByLabel("Settings")).toBeVisible();
  const name = `fd-adv-${Date.now()}`;
  const got = page.evaluate(
    (n) =>
      new Promise<string>((done, fail) => {
        const es = new EventSource(`/events?types=session.created`);
        const timer = setTimeout(() => {
          es.close();
          fail(new Error("no event"));
        }, 10_000);
        es.addEventListener("session.created", (ev) => {
          if ((ev as MessageEvent).data.includes(n)) {
            clearTimeout(timer);
            es.close();
            done((ev as MessageEvent).data);
          }
        });
      }),
    name,
  );
  await page.waitForTimeout(300);
  const created = await apiFetch(`${routerURL}/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, provider: "fake", workspace: process.env.MADS_WORKSPACE_ID }),
  });
  expect(created.status).toBeLessThan(300);
  const {
    session: { id },
  } = (await created.json()) as { session: { id: string } };
  expect(await got).toContain(name);
  // A plain same-origin fetch from the page, setting no header itself.
  const one = await page.evaluate(async (sid) => (await fetch(`/sessions/${sid}`)).status, id);
  expect(one).toBe(200);
  await apiFetch(`${routerURL}/sessions/${id}`, { method: "DELETE" });
});
