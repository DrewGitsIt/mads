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
 * The three rail behaviours no unit test can prove together, because each is
 * a claim about the *live* rail: the control-plane stream repainting a card
 * nobody is looking at, the search index reaching a field the card doesn't
 * even print, and the settled order holding across a real pointer, a real
 * focus move and a real SSE-driven refetch.
 *
 * Serial: every test reads the four sessions beforeAll creates, and each one
 * leaves the rail's recency order changed for the next (a finished turn and
 * a rename both restamp UpdatedAt). fullyParallel is off in this suite's
 * config today, but the convention is to declare serial up front rather than
 * depend on that — a block with shared beforeAll state must never be
 * splittable across workers.
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
   * The card a test drives a real turn through — the lowest of this block's
   * four, since a climber has to have somewhere to climb from. Never "delta":
   * it was seeded straight into the Data Service with no spawn, so there is no
   * runner to send to.
   *
   * A turn is what these journeys need because a turn END is now the only
   * thing that changes a card's row: the order reads last_turn_at, so a rename
   * (which moves updated_at alone) is deliberately not recency churn any more.
   */
  function pickClimber(snap: RailSnapshot): Key {
    return [...snap.mine].reverse().find((key) => key !== "delta")!;
  }

  /** Well clear of the 280px rail — the pointer leaving it is a settle boundary. */
  async function movePointerOffRail(page: Page): Promise<void> {
    await page.mouse.move(1200, 700);
  }

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

  test("hovering the rail holds every card's row until the pointer leaves", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);
    // The pointer has never entered the rail, so this order is settled.
    const before = await snapshot(page);
    const climber = pickClimber(before);

    // Engage the rail: pointer on a card, the way it is whenever anyone is
    // about to click one.
    await cardsOf(page).nth(before.rows[before.mine[0]]).hover();

    // A rename lands while the rail is engaged. It restamps UpdatedAt and
    // nothing else, so it is a repaint and not a move: the card takes its new
    // name in place, proving the refetch arrived, and there is no pending
    // reorder to hold — no ghost, because nothing was going to move.
    await renameSession(routerURL, ids[climber], `${names[climber]} renamed`);
    await expect(cardFor(page, climber)).toContainText("renamed");
    const ghost = railOf(page).getByRole("button", { name: /session[s]? moved/ });
    await expect(ghost).toHaveCount(0);
    expect(await snapshot(page)).toEqual(before);

    // Real recency churn, then: a turn that ends, which is the one thing that
    // restamps LastTurnAt. Now there IS a move, and it is offered rather than
    // performed under the cursor.
    await sendChatMessage(routerURL, ids[climber], `echo rail hold ${stamp}`);
    await expect(ghost).toBeVisible();
    await expect(ghost).toHaveText(/1 session moved/);
    // Nothing moved: not the card whose turn ended, not the cards it would
    // have pushed down, and nothing appeared or vanished.
    expect(await snapshot(page)).toEqual(before);

    // The pointer leaves — that is the boundary, and the held order applies.
    await movePointerOffRail(page);
    await expect(ghost).toHaveCount(0);
    const after = await snapshot(page);
    expect(after.mine[0]).toBe(climber);
    expect(after.rows[climber]).toBe(before.rows[before.mine[0]]);
  });

  test("the open session keeps its row while the rail reorders around it", async ({
    page,
    routerURL,
  }) => {
    await openRail(page, routerURL);
    const before = await snapshot(page);
    const climber = pickClimber(before);
    // The selected card needs cards on both sides: something above it, so the
    // climber can visibly pass it, and the climber below, so an unpinned rail
    // would push it down a row. "delta" is excluded — it is the dormant
    // search fixture, and opening it would 404 its live-output subscription.
    const selected = before.mine.find(
      (key, i) => i > 0 && key !== "delta" && before.rows[key] < before.rows[climber],
    )!;
    expect(selected, "a middle, live card to open").toBeTruthy();

    await cardFor(page, selected).click();
    const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
    await expect(composer).toBeVisible();
    // Clicking a card leaves it focused, and a focused rail is held exactly
    // like a hovered one. Moving focus into the composer and the pointer off
    // the rail is the quiet boundary this journey needs: from here on, an
    // order that doesn't apply is a pin, not a hold.
    await composer.click();
    await movePointerOffRail(page);
    const selectedRow = before.rows[selected];
    expect(await rowOf(page, selected)).toBe(selectedRow);

    // A turn end on the card below — the only churn that reorders the rail now.
    await sendChatMessage(routerURL, ids[climber], `echo rail pin ${stamp}`);
    // The rail took the update: the climber's own status line repaints.
    await expect(cardFor(page, climber).getByRole("status")).toContainText("Done · your turn");

    // The climber really did rise past the open session — the rail reordered,
    // it did not simply freeze...
    await expect.poll(() => rowOf(page, climber)).toBeLessThan(selectedRow);
    // ...and the open session is still on exactly the row it was on.
    expect(await rowOf(page, selected)).toBe(selectedRow);
    // Still open, too: the pin held the row, it didn't drop the selection.
    await expect(composer).toBeVisible();
  });
});
