import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";
import {
  createSession,
  createSessionNoSpawn,
  createWorkspace,
  renameSession,
  sendChatMessage,
  setupWorkspace,
} from "./lib/seed";

type Key = "alpha" | "bravo" | "charlie" | "delta";

/**
 * The rail behaviours no unit test can prove together, because each is a
 * claim about the *live* rail: the control-plane stream repainting a card
 * nobody is looking at, the search index reaching a field the card doesn't
 * even print, the user's order holding through a real SSE-driven refetch and
 * surviving a reload, and the ✓ really putting a session's agent to sleep.
 *
 * Serial: every test reads the four sessions beforeAll creates, and the later
 * ones leave the rail's order changed for the next (a drag, a check-off).
 * fullyParallel is off in this suite's config today, but the convention is to
 * declare serial up front rather than depend on that — a block with shared
 * beforeAll state must never be splittable across workers.
 *
 * Because the order moves as the block runs, no test hard-codes a row: each
 * reads the rail's current order first and picks its own cards out of it.
 */
test.describe("session rail", () => {
  test.describe.configure({ mode: "serial" });

  const stamp = `${Date.now()}`;
  /** Only ever appears in a directory path — never in a name or a workspace. */
  const dirToken = `zxq${stamp}`;
  const names: Record<Key, string> = {
    alpha: `rail alpha ${stamp}`,
    bravo: `rail bravo ${stamp}`,
    charlie: `rail charlie ${stamp}`,
    // The search target: neither its name nor its workspace carries dirToken.
    delta: `rail delta ${stamp}`,
  };
  const ids: Record<Key, string> = { alpha: "", bravo: "", charlie: "", delta: "" };
  let repoDir = "";

  test.beforeAll(async () => {
    const routerURL = process.env.MADS_ROUTER_URL!;
    const dataURL = process.env.MADS_DATA_URL!;

    // A workspace whose NAME shares nothing with its repo's path — the whole
    // reason the rail indexes both (railSearch.ts): "Main work" is not
    // derivable from ~/repo/mads, so a directory has to be searchable itself.
    repoDir = mkdtempSync(path.join(tmpdir(), `${dirToken}-repo-`));
    setupWorkspace(repoDir);
    const workspace = await createWorkspace(routerURL, {
      name: `Unrelated Home ${stamp}`,
      repos: [{ path: repoDir, primary: true }],
    });

    ids.alpha = (await createSession(routerURL, { name: names.alpha, provider: "fake" })).id;
    ids.bravo = (await createSession(routerURL, { name: names.bravo, provider: "fake" })).id;
    ids.charlie = (await createSession(routerURL, { name: names.charlie, provider: "fake" })).id;
    // Seeded through the Data Service: a spawned session's cwd is the runner's
    // to own, and in this topology it is always "." (see createSessionNoSpawn).
    ids.delta = (
      await createSessionNoSpawn(dataURL, {
        name: names.delta,
        provider: "fake",
        workspace: workspace.id,
        cwd: repoDir,
      })
    ).id;
  });

  const railOf = (page: Page) => page.locator('[aria-label="Session list"]');
  const cardsOf = (page: Page) => railOf(page).locator("[data-rail-card]");
  /** getByText would resolve to the name span; these journeys need the whole card. */
  const cardFor = (page: Page, key: Key) => cardsOf(page).filter({ hasText: names[key] }).first();

  /** A card's row in the rail, top to bottom. -1 when it isn't rendered. */
  async function rowOf(page: Page, key: Key): Promise<number> {
    const texts = await cardsOf(page).allTextContents();
    return texts.findIndex((t) => t.includes(names[key]));
  }

  interface RailSnapshot {
    rows: Record<Key, number>;
    /** Total cards, so a session appearing or vanishing can't pass as "nothing moved". */
    count: number;
    /** This block's four cards, top to bottom. */
    mine: Key[];
  }

  async function snapshot(page: Page): Promise<RailSnapshot> {
    const texts = await cardsOf(page).allTextContents();
    const rows = {} as Record<Key, number>;
    for (const key of Object.keys(names) as Key[]) {
      rows[key] = texts.findIndex((t) => t.includes(names[key]));
      expect(rows[key], `card "${names[key]}" is in the rail`).toBeGreaterThanOrEqual(0);
    }
    const mine = (Object.keys(names) as Key[]).sort((a, b) => rows[a] - rows[b]);
    return { rows, count: texts.length, mine };
  }

  /**
   * The lowest of this block's four live cards — the one that, under the old
   * last-reply order, a finished turn would have lifted. Never "delta": it was
   * seeded straight into the Data Service with no spawn, so there is no
   * runner to send to.
   */
  function pickLowestLive(snap: RailSnapshot): Key {
    return [...snap.mine].reverse().find((key) => key !== "delta")!;
  }

  /** Well clear of the 280px rail, so nothing is hovered while the rail updates. */
  async function movePointerOffRail(page: Page): Promise<void> {
    await page.mouse.move(1200, 700);
  }

  const activeList = (page: Page) => railOf(page).locator('[data-rail-list="active"]');
  const doneList = (page: Page) => railOf(page).locator('[data-rail-list="done"]');

  async function openRail(page: Page, routerURL: string): Promise<void> {
    await page.goto(routerURL);
    await expect(cardFor(page, "alpha")).toBeVisible();
    await expect(cardFor(page, "delta")).toBeVisible();
  }

  test("a card's status line follows a live turn", async ({ page, routerURL }) => {
    await openRail(page, routerURL);

    const card = cardFor(page, "alpha");
    const status = card.getByRole("status");
    // Live child, nothing in flight: the calm state the turn resolves back to.
    await expect(status).toContainText("Idle");

    // A real turn against the fake provider, driven from outside the browser:
    // nothing in this journey touches the page, so what follows is the shared
    // control-plane stream repainting a card the user isn't even looking at.
    // "echo slowly" paces the reply word by word (providers/fake/fake.go).
    await sendChatMessage(routerURL, ids.alpha, `echo slowly turn ${stamp}`);

    // Mid-turn first: the clause the runner itself reports for the main agent
    // (session.agent_state, emitted on send, before the reply streams). The
    // paced reply above is what keeps this window open long enough to read.
    await expect(status).toContainText("Working");

    // Then the turn lands on the card itself — no reload, no open conversation,
    // and no per-session /output socket behind it (useSessionAgentState folds
    // this out of the one shared session.* stream). The end of a turn is a
    // state transition, so it arrives whether or not the turn produced a
    // digest delta.
    await expect(status).toContainText("Done · your turn");

    await card.click();
    // The turn was real, not just an event: its reply is in the transcript.
    await expect(
      page.locator("#workspace-panel-conversation p").filter({ hasText: `you said turn ${stamp}` }),
    ).toBeVisible();
    // Opening the session is the reader receipt for the sticky "Done"
    // (useSessionAgentState's openSessionId) — the line falls back to calm.
    await expect(status).toContainText("Idle");
  });

  test("search finds a session by its directory when the workspace name doesn't", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);

    const search = page.getByLabel("Search sessions");
    await search.fill(dirToken);

    // One hit, and it is the session whose *cwd* carries the token: its name
    // is "rail delta <stamp>" and its workspace is "Unrelated Home <stamp>",
    // neither of which contains it.
    await expect(cardsOf(page)).toHaveCount(1);
    await expect(cardFor(page, "delta")).toBeVisible();

    // The reason line is the proof of which field answered: matchReason()
    // prints nothing when the name matched, and prefers workspace over dir —
    // so a "dir" reason means neither of those could have found this card.
    const reason = cardFor(page, "delta").locator('[data-match-reason="dir"]');
    await expect(reason).toBeVisible();
    await expect(reason).toContainText(repoDir);

    // The same hit under an explicit scope token, which can read nothing but
    // cwd — a bare substring could in principle have matched some other field.
    await search.fill(`dir:${repoDir}`);
    await expect(cardsOf(page)).toHaveCount(1);
    await expect(cardFor(page, "delta")).toBeVisible();

    // A query that matches nothing says what it looked at, rather than
    // leaving an unexplained empty rail.
    await search.fill(`nothing-matches-${stamp}`);
    await expect(cardsOf(page)).toHaveCount(0);
    await expect(railOf(page)).toContainText("Searched name, workspace, directory and branch");

    await page.getByLabel("Clear search").click();
    await expect(cardFor(page, "alpha")).toBeVisible();
  });

  test("a turn ending repaints its card in place — nothing moves a card but the user", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);
    await movePointerOffRail(page);
    const before = await snapshot(page);
    const lowest = pickLowestLive(before);

    // A rename and a finished turn: the two record writes that used to move
    // a card (updated_at, then last_turn_at). Neither may now.
    await renameSession(routerURL, ids[lowest], `${names[lowest]} renamed`);
    await expect(cardFor(page, lowest)).toContainText("renamed");
    await sendChatMessage(routerURL, ids[lowest], `echo rail still ${stamp}`);
    // The rail took the update: the card's own status line repaints...
    await expect(cardFor(page, lowest).getByRole("status")).toContainText("Done · your turn");
    // ...and nothing moved — with the pointer nowhere near the rail, which
    // is exactly when the old order applied a move at once.
    expect(await snapshot(page)).toEqual(before);
  });

  test("dragging a card's grip reorders the rail, and the order survives a reload", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);
    const before = await snapshot(page);
    const top = before.mine[0];
    const bottom = before.mine[before.mine.length - 1];

    // The grip only shows on hover, in the gutter left of the card.
    await cardFor(page, bottom).hover();
    const grip = cardFor(page, bottom).locator("[data-rail-grip]");
    await expect(grip).toBeVisible();
    // Onto the top half of the top card: "before" it.
    await grip.dragTo(cardFor(page, top), { targetPosition: { x: 40, y: 4 } });

    await expect.poll(() => rowOf(page, bottom)).toBe(before.rows[top]);
    expect(await rowOf(page, top)).toBe(before.rows[top] + 1);

    // On the record, not in this page: a reload reads it back from the Router.
    await page.reload();
    await expect(cardFor(page, bottom)).toBeVisible();
    expect(await rowOf(page, bottom)).toBe(before.rows[top]);
    expect(await rowOf(page, top)).toBe(before.rows[top] + 1);
  });

  test("✓ checks a session off into Done and puts its agent to sleep; bringing it back puts it on top", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);
    const before = await snapshot(page);
    // A live card that isn't already on top, so "back on top" is a real move.
    const target = before.mine.find((key, i) => i > 0 && key !== "delta")!;
    expect(target, "a live card below the top").toBeTruthy();

    await cardFor(page, target).hover();
    await page.getByRole("button", { name: `Check off ${names[target]}` }).click();

    const done = doneList(page).locator("[data-rail-card]").filter({ hasText: names[target] });
    await expect(done).toBeVisible();
    await expect(activeList(page).locator("[data-rail-card]").filter({ hasText: names[target] })).toHaveCount(0);
    // Done is Done-and-sleep: the record reports the agent stopped.
    await expect(done).toHaveAttribute("data-activity", "inactive");

    await done.hover();
    await page.getByRole("button", { name: `Bring back ${names[target]}` }).click();
    await expect(activeList(page).locator("[data-rail-card]").first()).toContainText(names[target]);
    await expect(doneList(page).locator("[data-rail-card]").filter({ hasText: names[target] })).toHaveCount(0);
  });
});
