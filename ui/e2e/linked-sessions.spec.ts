import { test, expect } from "./fixtures";
import { DIGEST_FIXTURE_TEXT } from "./lib/fastModelFixture";
import {
  createSession,
  injectLinkMessage,
  pollTranscriptContains,
  sendChatMessage,
  setConfig,
} from "./lib/seed";

test.describe("linked sessions: link UI behind the flag", () => {
  test.describe.configure({ mode: "serial" });

  let leaderId: string;
  let leaderName: string;
  let workerId: string;
  let workerName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    // The flag is false by default (Settings -> Advanced) — flip it here so
    // the chain buttons and the link editor exist at all for this journey.
    await setConfig(routerURL, { feature_flags: { linked_sessions: true } });

    leaderName = `e2e linked-sessions leader ${Date.now()}`;
    workerName = `e2e linked-sessions worker ${Date.now()}`;
    // In place, not isolated: the default workspace is a git repo, so by
    // default each session would get its own worktree — and linking two
    // existing sessions that each have their own copy is refused by design
    // (linked sessions must share one tree; the last test below covers
    // that refusal). This journey is about the link UI, so both work in
    // the shared checkout.
    const leader = await createSession(routerURL, { name: leaderName, provider: "fake", isolation: "current_dir" });
    const worker = await createSession(routerURL, { name: workerName, provider: "fake", isolation: "current_dir" });
    leaderId = leader.id;
    workerId = worker.id;
  });

  test("link editor -> link created, relationship shown, attributed inject and digest render", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');

    // The chain button lives in the card's action cluster, which hangs
    // outside the card and stays inert until the row is hovered — hover
    // first, as a real pointer would.
    await rail.getByText(leaderName, { exact: true }).hover();
    await rail.getByRole("button", { name: `Links for ${leaderName}` }).click();
    const dialog = page.getByRole("dialog", { name: `Links · ${leaderName}` });
    await expect(dialog).toBeVisible();

    // While the editor is open the rail is its picker: clicking the worker
    // links it under the centred leader and opens that link's editor.
    await rail.getByText(workerName, { exact: true }).click();
    const editor = dialog.getByRole("group", { name: `Edit link ${leaderName} → ${workerName}` });
    await expect(editor).toBeVisible();
    await expect(editor.getByText("New link")).toBeVisible();
    // Leads is the default for a session nobody leads yet.
    await expect(editor.getByRole("button", { name: "♛ Leads" })).toHaveAttribute("aria-pressed", "true");
    // The default cadence is Signals (on_signal), which pushes no per-turn
    // digests — this journey drives the per-turn digest pipeline, so pick
    // Follow explicitly.
    await editor.getByRole("radio", { name: /Follow/ }).click();

    const [linkResponse] = await Promise.all([
      page.waitForResponse((res) => res.request().method() === "POST" && res.url().endsWith("/links")),
      dialog.getByRole("button", { name: "Save 1 change" }).click(),
    ]);
    expect(linkResponse.ok()).toBeTruthy();
    const link = (await linkResponse.json()) as { id: string; grants: string[]; cadence: string };
    expect(link.grants).toContain("orchestrate");
    expect(link.cadence).toBe("per_turn");
    await expect(dialog).not.toBeVisible();

    // The rail states the relationship by nesting the subject under its
    // holder, and the editor shows the line.
    await expect(rail.locator('[class*="subjects"]').getByText(workerName, { exact: true })).toBeVisible();
    await rail.getByText(leaderName, { exact: true }).hover();
    await rail.getByRole("button", { name: `Links for ${leaderName}` }).click();
    await expect(
      dialog.getByRole("button", { name: new RegExp(`^${leaderName} leads ${workerName}, Follow\\.`) }),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();

    // Subject session's conversation: an injected message from the leader
    // renders as an attributed chip, never the subject's own bubble.
    await rail.getByText(workerName, { exact: true }).click();
    const workerPanel = page.locator("#workspace-panel-conversation");
    await injectLinkMessage(routerURL, link.id, "kick off the payments refactor");
    await expect(
      workerPanel.getByText(new RegExp(`⇄ from Leader · ${leaderName}`)),
    ).toBeVisible();
    await expect(workerPanel.getByText("kick off the payments refactor")).toBeVisible();

    // A real chat turn on the worker drives the real digest pipeline
    // (session.turn_ended -> digestwatch -> job.GenerateDigest -> Fast Model
    // fixture -> deliverDigest) rather than seeding a fake event directly.
    await sendChatMessage(routerURL, workerId, "echo digest trigger");
    await pollTranscriptContains(routerURL, leaderId, DIGEST_FIXTURE_TEXT);

    // Leader session's conversation: the digest card, not an inject chip,
    // since the leader is the link's granting session. The earlier inject
    // itself ended a turn on the worker too (the fake provider replies to
    // any delivered message), so per-turn cadence fans out a second digest
    // here alongside the one sendChatMessage's turn triggered — assert the
    // latest one rather than requiring exactly one.
    await rail.getByText(leaderName, { exact: true }).click();
    const leaderPanel = page.locator("#workspace-panel-conversation");
    // The digest header's source name sits in its own <b>, split from the
    // "digest ·" text node beside it — assert each separately rather than
    // one regex spanning both nodes.
    await expect(leaderPanel.locator("b", { hasText: workerName }).last()).toBeVisible();
    await expect(leaderPanel.getByText(DIGEST_FIXTURE_TEXT).last()).toBeVisible();
    await expect(
      leaderPanel.getByText("cadence: digest after each burst of activity").last(),
    ).toBeVisible();
  });

  test("Launchpad chooser disables Leader for a subject that's already led, naming its leader; Monitor still stages", async ({
    page,
    routerURL,
  }) => {
    // Depends on the previous test: worker is now led by leader (serial
    // mode). The Launchpad's staged chooser lists every session.
    await page.goto(routerURL);
    await page.getByRole("button", { name: "New session" }).click();
    await page.getByRole("button", { name: "More start options" }).click();
    await page.getByText("Link to session(s)…").click();

    const dialog = page.getByRole("dialog", { name: "Link sessions" });
    await expect(dialog).toBeVisible();
    // The subject list names each session's existing leader.
    const workerBox = dialog.getByRole("checkbox", { name: new RegExp(`^${workerName}`) });
    await expect(workerBox).toHaveAccessibleName(`${workerName} · led by ${leaderName}`);

    const leaderPreset = dialog.getByRole("button", { name: /^Leader/ });
    await expect(leaderPreset).toBeEnabled();
    await workerBox.check();

    // Picking the led worker disables Leader with its holder's name instead
    // of letting the create fail later; Monitor takes over and stays usable.
    await expect(leaderPreset).toBeDisabled();
    await expect(leaderPreset).toContainText(`Already led by ${leaderName}`);
    const monitorPreset = dialog.getByRole("button", { name: /^Monitor/ });
    await expect(monitorPreset).toBeEnabled();
    await expect(monitorPreset).toHaveAttribute("aria-selected", "true");

    await dialog.getByRole("button", { name: "Stage 1 link" }).click();
    await expect(dialog).not.toBeVisible();
  });
});

test.describe("linked sessions: two isolated sessions can't be linked", () => {
  test.describe.configure({ mode: "serial" });

  let aId: string;
  let bId: string;
  let aName: string;
  let bName: string;

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    await setConfig(routerURL, { feature_flags: { linked_sessions: true } });
    aName = `e2e isolated link a ${Date.now()}`;
    bName = `e2e isolated link b ${Date.now()}`;
    // Default isolation in the (git) default workspace: each gets its own
    // worktree. The Data Service refuses a link between them only once both
    // records carry worktree_dir, which the child writes before its
    // provider starts — wait for that rather than racing the spawn.
    const a = await createSession(routerURL, { name: aName, provider: "fake" });
    const b = await createSession(routerURL, { name: bName, provider: "fake" });
    aId = a.id;
    bId = b.id;
    for (const id of [a.id, b.id]) await pollWorktreeDir(routerURL, id);
  });

  test("the editor won't link them, and the server refuses with a sentence that says what to do instead", async ({
    page,
    routerURL,
  }) => {
    let posted = false;
    page.on("request", (req) => {
      if (req.method() === "POST" && req.url().endsWith("/links")) posted = true;
    });
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');

    await rail.getByText(aName, { exact: true }).hover();
    await rail.getByRole("button", { name: `Links for ${aName}` }).click();
    const dialog = page.getByRole("dialog", { name: `Links · ${aName}` });
    await expect(dialog).toBeVisible();

    // The other isolated session is dimmed, says why, and a click adds nothing.
    const bCard = rail.locator("[data-rail-card]", { hasText: bName });
    await expect(bCard).toHaveAttribute("data-link-edit", "blocked");
    await expect(bCard).toHaveAttribute("title", /own isolated copy of the repo/);
    await expect(bCard.getByText("separate worktree")).toBeVisible();
    // aria-disabled, so Playwright won't click it unforced; a pointer still can.
    await bCard.click({ force: true });
    await expect(dialog.getByRole("group")).toHaveCount(0);
    await expect(dialog.getByRole("button", { name: "No changes" })).toBeDisabled();
    expect(posted).toBe(false);

    // The server holds the same rule, and its refusal is a sentence.
    const res = await fetch(`${routerURL}/links`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ granting_session_id: aId, subject_session_id: bId, preset: "monitor" }),
    });
    expect(res.status).toBeGreaterThanOrEqual(400);
    expect(res.status).toBeLessThan(500);
    const body = (await res.json()) as { errors: { message: string }[] };
    const message = body.errors.map((e) => e.message).join(" ");
    expect(message).toMatch(/each have their own isolated copy of the repo/);
    expect(message).toMatch(/link them when starting the second session/);
    // Nothing was linked.
    const links = (await (await fetch(`${routerURL}/sessions/${aId}/links`)).json()) as unknown[];
    expect(links).toEqual([]);
  });
});

async function pollWorktreeDir(routerURL: string, id: string, timeoutMs = 15_000): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await fetch(`${routerURL}/sessions/${id}`);
    if (res.ok) {
      const s = (await res.json()) as { worktree_dir?: string };
      if (s.worktree_dir) return s.worktree_dir;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`session ${id} never got a worktree_dir`);
}
