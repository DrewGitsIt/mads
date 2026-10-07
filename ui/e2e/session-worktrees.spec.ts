import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { Locator, Page } from "@playwright/test";
import { test, expect } from "./fixtures";

/**
 * Session worktrees, end to end (docs/SESSION-WORKTREES-DESIGN.md): a
 * session started in the default (git) workspace works in its own folder
 * and branch; Commit and Land move its work into the user's checkout, with
 * every outcome a card in the conversation; a lost folder comes back; a base
 * change mergiraf merged stops Land for a check; delete takes the folder and
 * branch with it.
 *
 * The agent's edits are real tool writes by the fake provider: its built-in
 * "write-file <path> <content>" verb (providers/fake/fake.go) writes the
 * file under the session's cwd — the worktree — and emits a mutating
 * tool_use, so the session runner records it in the file-change ledger
 * exactly as it would a real agent's edit. Nothing here writes the session
 * folder or the ledger behind the product's back; the only direct disk
 * writes are to the user's own checkout (the primary), standing in for
 * the user.
 *
 * Screenshots under /tmp/wt-shots are walkthrough scaffolding, not
 * assertions; shot() never fails the test.
 */

const SHOTS = "/tmp/wt-shots";
const COMPOSER = "Message the agent — @ to mention, / for commands";

interface SessionRecord {
  id: string;
  name: string;
  worktree_dir?: string;
  worktree_branch?: string;
  worktree_base?: string;
  uncommitted_count?: number;
  unlanded_count?: number;
}

function git(dir: string, ...args: string[]): string {
  return execFileSync("git", ["-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", ...args], {
    cwd: dir,
    stdio: "pipe",
  })
    .toString()
    .trim();
}

async function getSession(routerURL: string, id: string): Promise<SessionRecord> {
  const res = await fetch(`${routerURL}/sessions/${id}`);
  if (!res.ok) throw new Error(`GET /sessions/${id}: ${res.status}`);
  return (await res.json()) as SessionRecord;
}

async function shot(page: Page, name: string, focus?: Locator): Promise<void> {
  try {
    mkdirSync(SHOTS, { recursive: true });
    // The conversation scrolls inside its own panel, so a card is only in
    // the shot once it has been scrolled into the panel's view — and the
    // panel may scroll smoothly, so let it settle first.
    if (focus) {
      await focus.scrollIntoViewIfNeeded({ timeout: 2_000 });
      await page.waitForTimeout(500);
    }
    // The app is a fixed, viewport-high layout, so the viewport is the whole
    // page; fullPage would re-lay it out and reset the panel's scroll.
    await page.screenshot({ path: path.join(SHOTS, name), timeout: 5_000 });
  } catch {
    // scaffolding only
  }
}

/**
 * shot, for a card that must really be in the picture: the conversation
 * may still be settling its own scroll when the card arrives (its Land
 * echo is swapped for the logged event's card), so scroll it in and wait
 * until it sits wholly inside the viewport before the screenshot.
 */
async function shotInView(page: Page, name: string, focus: Locator): Promise<void> {
  try {
    const viewport = page.viewportSize();
    await expect
      .poll(
        async () => {
          await focus.evaluate((el) => el.scrollIntoView({ block: "center" }));
          await page.waitForTimeout(300);
          const box = await focus.boundingBox();
          return Boolean(box && viewport && box.y >= 0 && box.y + box.height <= viewport.height);
        },
        { timeout: 5_000 },
      )
      .toBe(true);
    await shot(page, name);
  } catch {
    // scaffolding only
  }
}

/** Has git in dir got a merge in progress? */
function midMerge(dir: string): boolean {
  return existsSync(path.join(git(dir, "rev-parse", "--absolute-git-dir"), "MERGE_HEAD"));
}

test.describe("session worktrees: isolate, commit, land, recover, delete", () => {
  test.describe.configure({ mode: "serial" });
  // Each step drives real agent turns and git work; the default 30s is tight.
  test.describe.configure({ timeout: 60_000 });

  const primary = process.env.MADS_WORKSPACE_DIR!;
  const token = `${Date.now()}`;
  // Per-run file names, so the journey can repeat against one topology.
  const A = `wt-a-${token}.txt`;
  const C = `conflict-${token}.txt`;
  const G = `g-${token}.txt`;
  const M = `mergiraf-${token}.txt`;
  const goal = `write-file ${A} alpha ${token}`;
  let id: string;
  let name: string;
  let folder: string;
  let base: string;

  test("a. a session started from the Launchpad gets its own folder and branch", async ({
    page,
    routerURL,
  }) => {
    base = git(primary, "branch", "--show-current");
    const headBefore = git(primary, "rev-parse", "HEAD");

    await page.goto(routerURL);
    await page.getByLabel("Describe your goal").fill(goal);
    const [created] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/sessions")),
      page.getByRole("button", { name: "Start session" }).click(),
    ]);
    const body = (await created.json()) as {
      session: { id: string; name: string };
    };
    id = body.session.id;
    name = body.session.name;

    // The goal is the first message: the agent writes file A.
    await expect(page.getByText(`wrote ${A}`).first()).toBeVisible({
      timeout: 15_000,
    });

    const s = await getSession(routerURL, id);
    expect(s.worktree_dir).toBeTruthy();
    folder = s.worktree_dir!;
    expect(path.basename(path.dirname(folder))).toBe("session-worktrees");
    expect(path.basename(folder)).toBe(id);
    expect(s.worktree_branch).toBe(`mads/session/${id}`);
    expect(s.worktree_base).toBe(base);

    // Disk: the edit is in the session's folder, on its branch — and the
    // user's checkout didn't move or gain the file.
    expect(readFileSync(path.join(folder, A), "utf8")).toBe(`alpha ${token}\n`);
    expect(git(folder, "branch", "--show-current")).toBe(`mads/session/${id}`);
    expect(existsSync(path.join(primary, A))).toBe(false);
    expect(git(primary, "rev-parse", "HEAD")).toBe(headBefore);
    expect(git(primary, "branch", "--show-current")).toBe(base);
  });

  test("b. Commit… lists the session's file and commits it; the dock turns to Land", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();

    const work = page.locator("[data-work-state]");
    await expect(work).toHaveAttribute("data-work-state", "commit");
    await expect(work).toContainText("Commit…");
    // The dock's control shows the bare count; the words are in its title.
    await expect(work).toHaveAttribute("title", /^Commit 1 file\b/);
    await shot(page, "01-header-commit.png");

    await work.click();
    const dialog = page.getByRole("alertdialog", {
      name: "Commit this session's changes",
    });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel(`Include ${A}`)).toBeChecked();
    await dialog.getByLabel("Commit message").fill(`Add wt-a ${token}`);
    await shot(page, "02-commit-dialog.png");
    await dialog.getByRole("button", { name: "Commit 1 file" }).click();
    await expect(dialog).toHaveCount(0);

    const panel = page.locator("#workspace-panel-conversation");
    const committed = panel.locator('[data-worktree-card="committed"]').last();
    await expect(committed).toBeVisible();
    await expect(committed).toContainText("Committed 1 file");
    await expect(committed).toContainText(`Add wt-a ${token}`);
    await shot(page, "03-committed-card.png", committed);

    await expect(work).toHaveAttribute("data-work-state", "land");
    await expect(work).toContainText(`Land into ${base}`);
    await expect(work).toHaveAttribute("title", `Land 1 commit into ${base}`);
    await shot(page, "04-land-button.png");

    // Disk: committed on the session branch, with the Session trailer; the
    // folder is clean; the user's checkout still hasn't got it.
    expect(git(folder, "log", "-1", "--format=%s")).toBe(`Add wt-a ${token}`);
    expect(git(folder, "log", "-1", "--format=%B")).toContain(`Session: ${id}`);
    expect(git(folder, "status", "--porcelain")).toBe("");
    expect(existsSync(path.join(primary, A))).toBe(false);
  });

  test("c. Land merges into the base; the card survives a reload", async ({ page, routerURL }) => {
    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    const work = page.locator("[data-work-state]");
    await expect(work).toHaveAttribute("data-work-state", "land");
    await work.click();

    const panel = page.locator("#workspace-panel-conversation");
    const landed = panel.locator('[data-worktree-card="landed"]');
    await expect(landed).toHaveCount(1);
    await expect(landed).toContainText(`Landed into ${base}`);
    await expect(page.locator("[data-work-state]")).toHaveCount(0);
    await shot(page, "05-landed-card.png", landed);

    // Disk: the base branch has the file, as a --no-ff merge of the
    // session branch, and the user's checkout is on it and clean of merges.
    expect(git(primary, "show", `${base}:${A}`)).toBe(`alpha ${token}`);
    expect(readFileSync(path.join(primary, A), "utf8")).toBe(`alpha ${token}\n`);
    expect(git(primary, "log", "-1", "--format=%P", base).split(" ")).toHaveLength(2);
    expect(midMerge(primary)).toBe(false);
    expect((await getSession(routerURL, id)).unlanded_count ?? 0).toBe(0);

    // A reload opens on the Launchpad; reopen the session and the cards are
    // rebuilt from its event log.
    await page.reload();
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    await expect(panel.locator('[data-worktree-card="landed"]')).toHaveCount(1);
    await expect(panel.locator('[data-worktree-card="committed"]')).toHaveCount(1);
    await expect(page.locator("[data-work-state]")).toHaveCount(0);
  });

  test("d. a conflicting change on the base: Land stops, and Ask the agent drafts the fix", async ({
    page,
    routerURL,
  }) => {
    await agentWrites(routerURL, id, C, "from the session");
    await waitForSession(routerURL, id, (s) => (s.uncommitted_count ?? 0) === 1);
    await commitAll(routerURL, id, `Session's ${C}`);

    // The user commits a different file C on the base, in their checkout.
    writeFileSync(path.join(primary, C), "from base\n");
    git(primary, "add", C);
    git(primary, "commit", "-q", "-m", `Base's ${C}`);
    const baseHead = git(primary, "rev-parse", "HEAD");

    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    const work = page.locator("[data-work-state]");
    await expect(work).toHaveAttribute("data-work-state", "land");
    await work.click();

    const panel = page.locator("#workspace-panel-conversation");
    const conflicted = panel.locator('[data-worktree-card="conflicted"]');
    await expect(conflicted).toBeVisible();
    await expect(conflicted).toContainText(`${base} moved on: 1 file conflicts`);
    await expect(conflicted).toContainText(C);
    await shot(page, "06-conflict-card.png", conflicted);

    // The Land click opened the dock's ⎇ popover, which shows the same
    // outcome at the button — and its own Ask the agent (it covers the
    // card's corner while open, so that's the one to use).
    const outcome = page.locator('[data-testid="work-popover"] [data-testid="land-outcome"]');
    await expect(outcome).toHaveAttribute("data-kind", "conflicted");
    await outcome.getByRole("button", { name: "Ask the agent to resolve" }).click();
    await expect(page.locator('[data-testid="work-popover"]')).toHaveCount(0);
    const composer = page.getByPlaceholder(COMPOSER);
    await expect(composer).toHaveValue(
      `Merge ${base} into this branch and resolve the conflicts in ${C}, keeping both sides' changes. Then commit the merge.`,
    );
    // Drafted, never sent: nothing new went to the agent.
    await composer.fill("");

    // Disk: nothing merged anywhere, nobody left mid-merge.
    expect(git(primary, "rev-parse", "HEAD")).toBe(baseHead);
    expect(midMerge(primary)).toBe(false);
    expect(midMerge(folder)).toBe(false);
    expect(git(folder, "status", "--porcelain")).toBe("");
  });

  test("e. a dirty checkout on an overlapping file refuses Land and is left intact", async ({
    page,
    routerURL,
  }) => {
    // Resolve the conflict the way the agent would (take the base's C),
    // and change A, a file the base also has.
    await agentWrites(routerURL, id, C, "from base");
    await agentWrites(routerURL, id, A, `alpha changed by the session ${token}`);
    await waitForSession(routerURL, id, (s) => (s.uncommitted_count ?? 0) === 2);
    await commitAll(routerURL, id, `Take base's ${C}, change ${A}`);

    // The user has their own uncommitted edit to A in their checkout.
    const dirty = "the user's own edit\n";
    writeFileSync(path.join(primary, A), dirty);
    const baseHead = git(primary, "rev-parse", "HEAD");

    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    const work = page.locator("[data-work-state]");
    await expect(work).toHaveAttribute("data-work-state", "land");
    await work.click();

    const panel = page.locator("#workspace-panel-conversation");
    const refused = panel.locator('[data-worktree-card="refused"]').last();
    await expect(refused).toBeVisible();
    await expect(refused).toHaveAttribute("data-reason", "primary_dirty");
    await expect(refused).toContainText("Your checkout has staged or uncommitted changes");
    await expect(refused).toContainText(A);
    await shot(page, "07-refused-card.png", refused);

    // Disk: the user's checkout is exactly as they left it.
    expect(midMerge(primary)).toBe(false);
    expect(git(primary, "rev-parse", "HEAD")).toBe(baseHead);
    expect(readFileSync(path.join(primary, A), "utf8")).toBe(dirty);
    expect(git(primary, "status", "--porcelain")).toBe(`M ${A}`);
  });

  test("f. a session folder removed by hand comes back at the same path", async ({ page, routerURL }) => {
    // The user puts their checkout back, then someone deletes the session's folder.
    git(primary, "checkout", "--", A);
    rmSync(folder, { recursive: true, force: true });
    expect(existsSync(folder)).toBe(false);

    await page.goto(routerURL);
    await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
    const panel = page.locator("#workspace-panel-conversation");
    // The refusal's "Land again" is the user coming back after fixing it.
    await panel
      .locator('[data-worktree-card="refused"]')
      .last()
      .getByRole("button", { name: "Land again" })
      .click();

    await expect(panel.locator('[data-worktree-card="ensured"]')).toContainText(
      "Recreated this session's folder",
    );
    await expect(panel.locator('[data-worktree-card="landed"]')).toHaveCount(2);

    // Disk: the same path, on the same branch, and the work it held landed.
    expect(existsSync(folder)).toBe(true);
    expect(git(folder, "branch", "--show-current")).toBe(`mads/session/${id}`);
    expect(git(primary, "show", `${base}:${A}`)).toBe(`alpha changed by the session ${token}`);
    expect(midMerge(primary)).toBe(false);
  });

  test("g. a base change mergiraf merged: Land stops for a check, shows the changes, then lands", async ({
    page,
    routerURL,
  }) => {
    // A file both sides start from, landed so the base and the session share it.
    await agentWrites(routerURL, id, M, "common line");
    await waitForSession(routerURL, id, (s) => (s.uncommitted_count ?? 0) === 1);
    await commitAll(routerURL, id, `Add ${M}`);
    await landViaAPI(routerURL, id, "landed");

    // The session changes it; so does the user, on the base: plain git conflicts.
    await agentWrites(routerURL, id, M, "from the session");
    await waitForSession(routerURL, id, (s) => (s.uncommitted_count ?? 0) === 1);
    await commitAll(routerURL, id, `Session's ${M}`);
    writeFileSync(path.join(primary, M), "from the base\n");
    git(primary, "add", M);
    git(primary, "commit", "-q", "-m", `Base's ${M}`);
    const baseHead = git(primary, "rev-parse", "HEAD");

    // The services look for mergiraf at $MADS_MERGIRAF; a stand-in with its
    // calling convention goes there for this journey. It resolves the
    // conflict by keeping both sides' lines.
    const repoBefore = mergeDriverState(primary);
    const uninstall = installStandInMergiraf();
    try {
      await page.goto(routerURL);
      await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
      const work = page.locator("[data-work-state]");
      await expect(work).toHaveAttribute("data-work-state", "land");
      await work.click();

      const panel = page.locator("#workspace-panel-conversation");
      const auto = panel.locator('[data-worktree-card="auto-resolved"]');
      await expect(auto).toBeVisible();
      await expect(auto).toContainText(`${base} had moved on; mergiraf merged 1 file automatically`);
      await expect(auto).toContainText(`${M}. Check it, then Land again.`);
      await shotInView(page, "11-auto-resolved-card.png", auto);

      // Disk: merged and committed in the session's folder only.
      expect(git(primary, "rev-parse", "HEAD")).toBe(baseHead);
      expect(midMerge(primary)).toBe(false);
      expect(git(folder, "log", "-1", "--format=%P").split(" ")).toContain(baseHead);
      expect(git(folder, "status", "--porcelain")).toBe("");

      // Ask the agent drafts a message, and sends nothing. The Land click
      // left the dock's ⎇ popover open on the same outcome; its button is
      // the one in reach (and closes the popover).
      const outcome = page.locator('[data-testid="work-popover"] [data-testid="land-outcome"]');
      await expect(outcome).toHaveAttribute("data-kind", "autoResolved");
      await outcome.getByRole("button", { name: "Ask the agent to check" }).click();
      await expect(page.locator('[data-testid="work-popover"]')).toHaveCount(0);
      const composer = page.getByPlaceholder(COMPOSER);
      await expect(composer).toHaveValue(new RegExp(`resolved ${M.replace(/\./g, "\\.")} automatically`));
      await composer.fill("");

      // See the changes: the Files tab, on the merge commit's file.
      await auto.getByRole("button", { name: "See the changes" }).click();
      const files = page.locator("#workspace-panel-files");
      await expect(files).toBeVisible();
      await expect(files).toContainText(`Merge ${base} into mads/session/${id}`);
      await expect(files).toContainText(M);
      await expect(files.locator(".monaco-diff-editor").first()).toBeVisible({ timeout: 15_000 });
      const mergeCommit = git(folder, "rev-parse", "--short=7", "HEAD");
      await expect(files.getByText(`Diff from commit ${mergeCommit}`)).toBeVisible();
      await shot(page, "12-auto-resolved-changes.png");

      // Back in the conversation, Land again lands it.
      await page.locator("#workspace-tab-conversation").click();
      await auto.getByRole("button", { name: "Land again" }).click();
      await expect(panel.locator('[data-worktree-card="landed"]')).toHaveCount(4);
      expect(readFileSync(path.join(primary, M), "utf8")).toMatch(/from the (session|base)\nfrom the (base|session)\n/);
      expect(midMerge(primary)).toBe(false);
      expect((await getSession(routerURL, id)).unlanded_count ?? 0).toBe(0);
      // mergiraf reached only Land's own merges: nothing was registered in
      // the repo, where the user's own merges would pick it up.
      expect(mergeDriverState(primary)).toBe(repoBefore);
    } finally {
      uninstall();
    }
  });

  test("h. delete warns about unsaved work, then takes the folder and branch", async ({
    page,
    routerURL,
  }) => {
    await agentWrites(routerURL, id, G, "left behind");
    await waitForSession(routerURL, id, (s) => (s.uncommitted_count ?? 0) === 1);

    await page.goto(routerURL);
    // Deleting unmounts the workspace while its SSE subscription may still
    // refetch the now-gone session (sessions.spec's documented race).
    page.allowStatus(id);
    const rail = page.locator('[aria-label="Session list"]');
    const row = rail.getByText(name, { exact: true });
    const card = row.locator('xpath=ancestor::div[@role="button"][1]');
    // The rail's hollow marker: this session has uncommitted changes.
    await expect(card.locator('[data-work-mark="commit"]')).toBeVisible();
    await shot(page, "09-rail-markers.png");

    await row.hover();
    await rail.getByLabel(`Archive ${name}`).click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toContainText(`Delete “${name}”?`);
    await expect(dialog).toContainText("1 uncommitted file will be discarded");
    await shot(page, "08-delete-warning.png");
    await dialog.getByRole("button", { name: /^((Close|Stop) and delete|Delete)$/ }).click();
    await expect(rail.getByText(name, { exact: true })).toHaveCount(0);

    // Disk: folder and branch gone; the user's checkout keeps what landed.
    await expect.poll(() => existsSync(folder), { timeout: 10_000 }).toBe(false);
    expect(git(primary, "branch", "--list", `mads/session/${id}`)).toBe("");
    expect(git(primary, "worktree", "list")).not.toContain(folder);
    expect(readFileSync(path.join(primary, A), "utf8")).toBe(`alpha changed by the session ${token}\n`);
    expect(git(primary, "status", "--porcelain")).toBe("");

    // Walkthrough only: where "Write commit messages for me" lives.
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByRole("button", { name: "General" }).click();
    await page
      .getByText("Write commit messages for me")
      .scrollIntoViewIfNeeded()
      .catch(() => {});
    await shot(page, "10-settings-toggle.png");
  });
});

/** POST /sessions/{id}/land — setup, where the card isn't under test. */
async function landViaAPI(routerURL: string, id: string, want: string): Promise<void> {
  const res = await fetch(`${routerURL}/sessions/${id}/land`, { method: "POST" });
  if (!res.ok) throw new Error(`land: ${res.status} ${await res.text()}`);
  const body = (await res.json()) as { outcome: string };
  if (body.outcome !== want) throw new Error(`land: ${JSON.stringify(body)}, want ${want}`);
}

/**
 * Puts a stand-in for mergiraf where the services look for it
 * ($MADS_MERGIRAF, set by the topology). It takes mergiraf's arguments and
 * resolves every conflict by keeping both sides' lines (git merge-file
 * --union). Returns the undo.
 */
function installStandInMergiraf(): () => void {
  const bin = process.env.MADS_MERGIRAF!;
  mkdirSync(path.dirname(bin), { recursive: true });
  writeFileSync(bin, '#!/bin/sh\nexec git merge-file --union "$4" "$3" "$5"\n', { mode: 0o755 });
  return () => rmSync(bin, { force: true });
}

/** dir's repository's merge.* config and shared attributes file: where a merge driver registration would show. */
function mergeDriverState(dir: string): string {
  let config = "";
  try {
    config = git(dir, "config", "--local", "--get-regexp", "^merge\\.");
  } catch {
    // none
  }
  const common = path.resolve(dir, git(dir, "rev-parse", "--git-common-dir"));
  const attrs = path.join(common, "info", "attributes");
  return `${config}\0${existsSync(attrs) ? readFileSync(attrs, "utf8") : ""}`;
}

/** POST /sessions/{id}/commit with every uncommitted session file — setup, where the dialog isn't under test. */
async function commitAll(routerURL: string, id: string, message: string): Promise<void> {
  const res = await fetch(`${routerURL}/sessions/${id}/commit`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
  });
  if (!res.ok) throw new Error(`commit: ${res.status} ${await res.text()}`);
}

async function waitForSession(
  routerURL: string,
  id: string,
  ok: (s: SessionRecord) => boolean,
  timeoutMs = 15_000,
): Promise<SessionRecord> {
  const deadline = Date.now() + timeoutMs;
  let last: SessionRecord | undefined;
  while (Date.now() < deadline) {
    last = await getSession(routerURL, id);
    if (ok(last)) return last;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`session ${id} never reached the expected state: ${JSON.stringify(last)}`);
}

/**
 * One agent turn that writes file: sends the fake's write-file verb (a
 * 409 means the previous turn is still finishing — try again, as the
 * composer's queue would) and waits for this turn's own "wrote <file>"
 * reply, counted so an earlier turn's identical reply can't satisfy it.
 */
async function agentWrites(routerURL: string, id: string, file: string, content: string): Promise<void> {
  const wrote = async () => {
    const res = await fetch(`${routerURL}/sessions/${id}/transcript`);
    const messages = res.ok ? ((await res.json()) as { text?: string }[]) : [];
    return messages.filter((m) => m.text?.trim() === `wrote ${file}`).length;
  };
  const before = await wrote();
  const deadline = Date.now() + 15_000;
  for (;;) {
    const res = await fetch(`${routerURL}/sessions/${id}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: `write-file ${file} ${content}` }),
    });
    if (res.ok) break;
    if (res.status !== 409 || Date.now() > deadline)
      throw new Error(`send: ${res.status} ${await res.text()}`);
    await new Promise((r) => setTimeout(r, 200));
  }
  await expect.poll(wrote, { timeout: 15_000 }).toBeGreaterThan(before);
}
