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
    // the rail multi-select/chooser/badges exist at all for this journey.
    await setConfig(routerURL, { feature_flags: { linked_sessions: true } });

    leaderName = `e2e linked-sessions leader ${Date.now()}`;
    workerName = `e2e linked-sessions worker ${Date.now()}`;
    const leader = await createSession(routerURL, { name: leaderName, provider: "fake" });
    const worker = await createSession(routerURL, { name: workerName, provider: "fake" });
    leaderId = leader.id;
    workerId = worker.id;
  });

  test("multi-select -> chooser -> link created, relationship shown, attributed inject and digest render", async ({
    page,
    routerURL,
  }) => {
    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');

    // Link mode starts from the leader row's 🔗 action, which anchors and
    // auto-selects that row (so the leader is the chooser's default holder);
    // other rows then toggle by clicking them, and the link bar's
    // "Choose mode" opens the chooser once ≥2 are selected. The holder radio
    // is still selected explicitly below so this doesn't rely on ordering.
    // The 🔗 lives in the action cluster, which hangs outside the card and
    // stays inert until the row is hovered — hover first, as a real pointer
    // would, or the cluster never anchors and the click hit-tests through to
    // the card underneath.
    await rail.getByText(leaderName, { exact: true }).hover();
    await rail.getByLabel(`Add ${leaderName} to link selection`).click();
    await rail.getByText(workerName, { exact: true }).click();
    await expect(rail.getByLabel(`Remove ${workerName} from link selection`)).toBeChecked();
    await rail.getByRole("button", { name: "Choose mode" }).click();

    const dialog = page.getByRole("dialog", { name: "Link sessions" });
    await expect(dialog).toBeVisible();

    // Leader is the default preset, but this journey exercises the Leader
    // preset card explicitly rather than relying on default state.
    await dialog.getByText("Leader", { exact: true }).click();

    // The cadence default is Wake on signals (on_signal), which pushes no
    // per-turn digests — this journey drives the per-turn digest pipeline,
    // so pick Follow intently explicitly.
    await dialog.getByText("Follow intently", { exact: true }).click();

    // Each radio's accessible name is "{own name} ⟶ {others}" (from the
    // label's full text content) — anchor on the prefix so the row whose own
    // name is leaderName isn't confused with the other row, whose fan-out
    // span also mentions leaderName.
    await dialog.getByRole("radio", { name: new RegExp(`^${leaderName} ⟶`) }).check();

    const [linkResponse] = await Promise.all([
      page.waitForResponse(
        (res) => res.request().method() === "POST" && res.url().endsWith("/links"),
      ),
      dialog.getByRole("button", { name: "Create 1 link" }).click(),
    ]);
    expect(linkResponse.ok()).toBeTruthy();
    const link = (await linkResponse.json()) as { id: string };

    await expect(dialog).not.toBeVisible();

    // The name span sits in .rowTop and the badge in the sibling .rowMeta,
    // so the shared ancestor is two levels up from the name text.
    const leaderRow = rail.getByText(leaderName, { exact: true }).locator("../..");
    await expect(leaderRow.getByText("LEADER", { exact: true })).toBeVisible();
    const workerRow = rail.getByText(workerName, { exact: true }).locator("../..");
    await expect(workerRow.getByText("LINKED", { exact: true })).toBeVisible();

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

  test("chooser disables Leader for a subject that's already led, naming its leader; Monitor still creates", async ({
    page,
    routerURL,
  }) => {
    // Depends on the previous test: worker is now led by leader (serial mode).
    const thirdName = `e2e linked-sessions third ${Date.now()}`;
    await createSession(routerURL, { name: thirdName, provider: "fake" });

    await page.goto(routerURL);
    const rail = page.locator('[aria-label="Session list"]');
    await rail.getByText(thirdName, { exact: true }).hover();
    await rail.getByLabel(`Add ${thirdName} to link selection`).click();
    await rail.getByText(workerName, { exact: true }).click();
    await expect(rail.getByLabel(`Remove ${workerName} from link selection`)).toBeChecked();
    await rail.getByRole("button", { name: "Choose mode" }).click();

    const dialog = page.getByRole("dialog", { name: "Link sessions" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("radio", { name: new RegExp(`^${thirdName} ⟶`) }).check();

    // worker is the subject and already has a leader: Leader is disabled
    // and says who leads it, instead of failing on submit.
    const leaderPreset = dialog.getByRole("button", { name: /^Leader/ });
    await expect(leaderPreset).toBeDisabled();
    await expect(leaderPreset).toContainText(`Already led by ${leaderName}`);
    await expect(dialog.getByText(`· led by ${leaderName}`)).toBeVisible();
    const monitorPreset = dialog.getByRole("button", { name: /^Monitor/ });
    await expect(monitorPreset).toBeEnabled();
    await expect(monitorPreset).toHaveAttribute("aria-selected", "true");

    const [linkResponse] = await Promise.all([
      page.waitForResponse(
        (res) => res.request().method() === "POST" && res.url().endsWith("/links"),
      ),
      dialog.getByRole("button", { name: "Create 1 link" }).click(),
    ]);
    expect(linkResponse.ok()).toBeTruthy();
    const created = (await linkResponse.json()) as { grants: string[] };
    expect(created.grants).toEqual(["read"]);
    await expect(dialog).not.toBeVisible();
  });
});
