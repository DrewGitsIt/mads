import { test, expect } from "./fixtures";
import { createSession, setConfig } from "./lib/seed";

const COMPOSER = "Message the agent — @ to mention, / for commands";

test.describe("session commands: /new and /new-follower", () => {
  test.describe.configure({ mode: "serial" });

  let thisId: string;
  let thisName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    // The link variants exist only behind the flag.
    await setConfig(routerURL, { feature_flags: { linked_sessions: true } });
    thisName = `e2e new-cmd origin ${Date.now()}`;
    // In place: a worktree-isolated origin would make the follower join its
    // worktree, which the vitest suite covers; this journey is the two paths.
    const s = await createSession(routerURL, { name: thisName, provider: "fake", isolation: "current_dir" });
    thisId = s.id;
  });

  test("/new + Enter starts in the background; the toast's Open goes there", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(thisName, { exact: true }).click();
    const header = page.locator('[title="Rename session"]');
    await expect(header).toHaveText(thisName);

    const goal = `echo new background ${Date.now()}`;
    await page.getByPlaceholder(COMPOSER).fill(`/new ${goal}`);
    await page.getByPlaceholder(COMPOSER).press("Enter");

    const card = page.getByTestId("new-session-card");
    await expect(card).toBeVisible();
    const goalField = card.getByLabel("Goal for the new session");
    await expect(goalField).toHaveValue(goal);
    // Started from this session's values: in place, like the origin.
    await expect(card.getByRole("button", { name: /isolation\s+Current directory/ })).toBeVisible();

    const created = page.waitForResponse(
      (res) => res.request().method() === "POST" && new URL(res.url()).pathname === "/sessions",
    );
    await goalField.press("Enter");
    expect((await created).ok()).toBeTruthy();

    await expect(card).not.toBeVisible();
    await expect(page.getByText(`Started ${goal}`)).toBeVisible();
    // Still here.
    await expect(header).toHaveText(thisName);
    await expect(rail.getByText(goal, { exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Open", exact: true }).click();
    await expect(header).toHaveText(goal);
    // The goal went out as its first message and the fake agent answered.
    await expect(page.getByText(`you said new background`)).toBeVisible();
  });

  test("/new-follower + ⌘Enter creates the led session and opens it", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(thisName, { exact: true }).click();
    const header = page.locator('[title="Rename session"]');
    await expect(header).toHaveText(thisName);

    const goal = `echo new follower ${Date.now()}`;
    await page.getByPlaceholder(COMPOSER).fill(`/new-follower ${goal}`);
    await page.getByPlaceholder(COMPOSER).press("Enter");

    const card = page.getByTestId("new-session-card");
    await expect(card).toBeVisible();
    await expect(card.getByText("── leads ──▶")).toBeVisible();
    await card.getByRole("radio", { name: "per turn" }).click();

    const created = page.waitForResponse(
      (res) => res.request().method() === "POST" && new URL(res.url()).pathname === "/sessions",
    );
    await card.getByLabel("Goal for the new session").press("ControlOrMeta+Enter");
    const res = await created;
    expect(res.ok()).toBeTruthy();
    const body = (await res.json()) as { session: { id: string } };
    const newId = body.session.id;

    await expect(header).toHaveText(goal);
    await expect(page.getByText(`you said new follower`)).toBeVisible();

    const links = (await (await fetch(`${routerURL}/sessions/${thisId}/links`)).json()) as {
      granting_session_id: string;
      subject_session_id: string;
      grants: string[];
      cadence: string;
    }[];
    const link = links.find((l) => l.subject_session_id === newId);
    expect(link).toMatchObject({ granting_session_id: thisId, cadence: "per_turn" });
    expect(link!.grants).toContain("orchestrate");
  });
});
