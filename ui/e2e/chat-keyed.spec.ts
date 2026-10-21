import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { createSession, injectLinkMessage, sendChatMessage, setConfig } from "./lib/seed";
import {
  CURSOR_PROMPT,
  CURSOR_WORDS,
  DUP_BLOCKS,
  DUP_PROMPT,
  DUP_TOOL,
  HELD_BLOCKS,
  HELD_PROMPT,
  HELD_TOOL,
  JOIN_CASES,
  LAG_PROMPTS,
  LAG_WORDS,
  SWITCH_PROMPTS,
  SWITCH_WORDS,
} from "./lib/sessionScenario";

/**
 * The keyed chat pane (docs/CHAT-STREAM-DESIGN.md, "Test plan"): the
 * 2026-10-20 repro recipes as regressions, against the fake provider's
 * scripted turns (lib/sessionScenario.ts). Each rule streams slowly enough
 * that the live phase is observable; nothing waits a fixed time where a
 * condition can be awaited.
 */

const COMPOSER = "Message the agent — @ to mention, / for commands";
/** STREAM_REATTACH_EVENT (src/conversation/streamReattachSignal.ts). */
const REATTACH_EVENT = "mads:reattach-stream";

/** The in-page helpers installHelpers puts on window.__c5. */
interface Helpers {
  /** Text with whitespace removed, the hover timestamp left out. */
  flat(el: Element): string;
  /** Markup, the hover timestamp left out. */
  html(el: Element): string;
  /** The user bubble whose text contains `text`. */
  user(text: string): HTMLElement | undefined;
  /** User bubbles reading exactly `text`. */
  exactUsers(text: string): number;
  /** The rows after the user bubble containing `text`, up to the next user bubble. */
  replyRows(text: string): HTMLElement[];
  /** Occurrences of `needle` in the conversation rows' text. */
  count(needle: string): number;
  /** Words matching `re` in the conversation rows' text, in order. */
  words(re: string): string[];
  /** Watching a user bubble: shown at least once, and how often it was absent after. */
  watch: { target: string; seen: boolean; gaps: number };
}

/**
 * Installed before the app loads. A stored row brings the hover timestamp
 * (<time>); a live one has none, by design, so text and markup comparisons
 * leave it out. The MutationObserver records every moment, after the watched
 * text first showed as a user bubble, that no user bubble carries it.
 */
function installHelpers() {
  const strip = (el: Element) => {
    const clone = el.cloneNode(true) as HTMLElement;
    clone.querySelectorAll("time").forEach((n) => n.remove());
    return clone;
  };
  const users = () => [...document.querySelectorAll<HTMLElement>("[data-chat-rows] > [data-user-msg]")];
  const rowsText = () => document.querySelector("[data-chat-rows]")?.textContent ?? "";
  const c5: Helpers = {
    flat: (el) => (strip(el).textContent ?? "").replace(/\s+/g, ""),
    html: (el) => strip(el).innerHTML,
    user: (text) => users().find((el) => el.textContent?.includes(text)),
    exactUsers: (text) => users().filter((el) => strip(el).textContent?.trim() === text).length,
    replyRows: (text) => {
      const out: HTMLElement[] = [];
      let el = c5.user(text)?.nextElementSibling;
      for (; el && !el.hasAttribute("data-user-msg"); el = el.nextElementSibling) out.push(el as HTMLElement);
      return out;
    },
    count: (needle) => rowsText().split(needle).length - 1,
    words: (re) => rowsText().match(new RegExp(re, "g")) ?? [],
    watch: { target: "", seen: false, gaps: 0 },
  };
  (window as unknown as { __c5: Helpers }).__c5 = c5;
  new MutationObserver(() => {
    const w = c5.watch;
    if (!w.target) return;
    if (c5.user(w.target)) w.seen = true;
    else if (w.seen) w.gaps++;
  }).observe(document, { subtree: true, childList: true, characterData: true });
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(installHelpers);
});

const uniq = () => `${Date.now()}-${Math.floor(Math.random() * 1e6)}`;

function conversation(page: Page) {
  return page.locator("#workspace-panel-conversation");
}

/** The turn is over: the working indicator is gone. */
async function expectIdle(page: Page, timeout = 30_000) {
  await expect(
    conversation(page).getByRole("status", { name: /^(Working…|Using .*…|Compacting…)$/ }),
  ).toHaveCount(0, { timeout });
}

/** Two animation frames: React has committed whatever was dispatched. */
async function nextFrames(page: Page) {
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
}

/** Opens a session from the rail and waits for its composer. */
async function openFromRail(page: Page, name: string) {
  await page.locator('[aria-label="Session list"]').getByText(name, { exact: true }).click();
  await expect(page.getByPlaceholder(COMPOSER)).toBeVisible();
}

async function send(page: Page, text: string) {
  const composer = page.getByPlaceholder(COMPOSER);
  await composer.fill(text);
  await composer.press("Enter");
}

function isTranscriptPage(url: string, sessionId: string) {
  const u = new URL(url);
  return u.pathname.endsWith(`/sessions/${sessionId}/transcript`) && u.searchParams.get("v") === "2";
}

function isOutputV2(url: string, sessionId: string) {
  const u = new URL(url);
  return u.pathname.endsWith(`/sessions/${sessionId}/output`) && u.searchParams.get("v") === "2";
}

async function newSession(routerURL: string, label: string) {
  const name = `c5 ${label} ${uniq()}`;
  const session = await createSession(routerURL, { name, provider: "fake", isolation: "current_dir" });
  return { ...session, name };
}

/** A session in the shared checkout (no worktree to make), open in the pane with its first page loaded. */
async function openNewSession(page: Page, routerURL: string, label: string) {
  const session = await newSession(routerURL, label);
  await page.goto(routerURL);
  const firstPage = page.waitForResponse((r) => isTranscriptPage(r.url(), session.id));
  await openFromRail(page, session.name);
  await firstPage;
  return session;
}

/** What "Restart agent" does to the pane: reopen the stream from its last frame. */
async function reattach(page: Page, sessionId: string) {
  const reopened = page.waitForRequest((r) => isOutputV2(r.url(), sessionId) && r.url().includes("after="));
  await page.evaluate(
    ([type, id]) => window.dispatchEvent(new CustomEvent(type, { detail: { sessionId: id } })),
    [REATTACH_EVENT, sessionId] as const,
  );
  return reopened;
}

/** The page is hidden and loses focus, then comes back: what a tab switch does. */
async function blurAndRefocus(page: Page) {
  const setVisibility = (state: "hidden" | "visible") =>
    page.evaluate((s) => {
      Object.defineProperty(document, "visibilityState", { configurable: true, get: () => s });
      Object.defineProperty(document, "hidden", { configurable: true, get: () => s === "hidden" });
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event(s === "hidden" ? "blur" : "focus"));
    }, state);
  await setVisibility("hidden");
  // Hidden for a beat, as a real tab switch is; no condition marks "long enough".
  await page.waitForTimeout(400);
  await setVisibility("visible");
}

const c5 = (page: Page) => ({
  replyRows: (prompt: string) =>
    page.evaluate((p) => {
      const h = (window as unknown as { __c5: Helpers }).__c5;
      return h.replyRows(p).map((el) => h.flat(el));
    }, prompt),
  exactUsers: (text: string) =>
    page.evaluate((t) => (window as unknown as { __c5: Helpers }).__c5.exactUsers(t), text),
  count: (needle: string) => page.evaluate((n) => (window as unknown as { __c5: Helpers }).__c5.count(n), needle),
  words: (re: string) => page.evaluate((r) => (window as unknown as { __c5: Helpers }).__c5.words(r), re),
  watch: (target: string) =>
    page.evaluate((t) => {
      (window as unknown as { __c5: Helpers }).__c5.watch.target = t;
    }, target),
  watched: () => page.evaluate(() => ({ ...(window as unknown as { __c5: Helpers }).__c5.watch })),
});

test.describe("keyed chat pane", () => {
  test.beforeAll(async () => {
    // Native pace: text shows as it arrives. (The specs wait for whole text
    // either way; this keeps them from waiting on a reading pace.)
    await setConfig(process.env.MADS_ROUTER_URL!, { stream_speed: "off" });
  });

  test("text join R4A–E: the live bubble is the settled bubble, element and markup", async ({ page, routerURL }) => {
    test.setTimeout(90_000);
    const session = await openNewSession(page, routerURL, "join");
    const live = new Map<string, string>();

    for (const c of JOIN_CASES) {
      // Hold the turn-end page fetch until the live bubble is captured: the
      // capture is then the stream's own rendering, before a stored row
      // could replace anything.
      let release!: () => void;
      const gate = new Promise<void>((r) => (release = r));
      const held = (url: URL) => isTranscriptPage(url.toString(), session.id);
      await page.route(held, async (route) => {
        await gate;
        await route.continue();
      });

      await send(page, c.prompt);
      await page.waitForFunction(
        ([p, flat]) => {
          const h = (window as unknown as { __c5: Helpers }).__c5;
          const rows = h.replyRows(p);
          return rows.length === 1 && h.flat(rows[0]) === flat;
        },
        [c.prompt, c.flat] as const,
        { timeout: 15_000 },
      );
      const captured = await page.evaluate((p) => {
        const h = (window as unknown as { __c5: Helpers }).__c5;
        const bubble = h.replyRows(p)[0] as HTMLElement & { __c5tag?: string };
        bubble.__c5tag = p;
        (window as unknown as Record<string, unknown>)[`__c5el:${p}`] = bubble;
        return h.html(bubble);
      }, c.prompt);
      live.set(c.prompt, captured);

      const merged = page.waitForResponse((r) => isTranscriptPage(r.url(), session.id));
      release();
      await merged;
      await page.unroute(held);
      await expectIdle(page);
      await nextFrames(page);

      const after = await page.evaluate((p) => {
        const h = (window as unknown as { __c5: Helpers }).__c5;
        const bubble = (window as unknown as Record<string, HTMLElement & { __c5tag?: string }>)[`__c5el:${p}`];
        return { connected: bubble.isConnected, tag: bubble.__c5tag, html: h.html(bubble), same: h.replyRows(p)[0] === bubble };
      }, c.prompt);
      expect(after.connected, `${c.name}: the live bubble stayed mounted`).toBe(true);
      expect(after.tag, `${c.name}: the same element`).toBe(c.prompt);
      expect(after.same, `${c.name}: still the reply's bubble`).toBe(true);
      expect(after.html, `${c.name}: settled markup equals live markup`).toBe(captured);
      expect(await c5(page).replyRows(c.prompt), `${c.name}: one bubble for the reply`).toEqual([c.flat]);
    }

    // A fresh mount reads only stored rows: the same markup again.
    await page.reload();
    await openFromRail(page, session.name);
    for (const c of JOIN_CASES) {
      await expect.poll(() => c5(page).replyRows(c.prompt)).toEqual([c.flat]);
      const html = await page.evaluate((p) => {
        const h = (window as unknown as { __c5: Helpers }).__c5;
        return h.html(h.replyRows(p)[0]);
      }, c.prompt);
      expect(html, `${c.name}: stored markup after a reload equals live markup`).toBe(live.get(c.prompt));
    }
  });

  test.describe("the user bubble never vanishes while its row is stored late", () => {
    const lastWord = LAG_WORDS[LAG_WORDS.length - 1].trim();

    test("(a) the send that starts the session", async ({ page, routerURL }) => {
      await page.goto(routerURL);
      const goal = `${LAG_PROMPTS.start} ${uniq()}`;
      await c5(page).watch(goal);
      await page.getByLabel("Describe your goal").fill(goal);
      await page.getByRole("button", { name: "Start session" }).click();
      await expect.poll(async () => (await c5(page).watched()).seen).toBe(true);
      await expect(conversation(page).getByText(lastWord)).toBeVisible({ timeout: 20_000 });
      await expectIdle(page);
      await nextFrames(page);
      expect((await c5(page).watched()).gaps).toBe(0);
      expect(await c5(page).exactUsers(goal)).toBe(1);
    });

    test("(b) a visibility change mid-turn", async ({ page, routerURL }) => {
      await openNewSession(page, routerURL, "lag visibility");
      const text = `${LAG_PROMPTS.visibility} ${uniq()}`;
      await c5(page).watch(text);
      await send(page, text);
      await expect.poll(async () => (await c5(page).watched()).seen).toBe(true);
      // Well inside the 2.5 s store lag: the row isn't stored yet.
      await blurAndRefocus(page);
      await expect(conversation(page).getByText(lastWord)).toBeVisible({ timeout: 20_000 });
      await expectIdle(page);
      await nextFrames(page);
      expect((await c5(page).watched()).gaps).toBe(0);
      expect(await c5(page).exactUsers(text)).toBe(1);
    });

    test("(c) a stream reconnect mid-turn", async ({ page, routerURL }) => {
      const session = await openNewSession(page, routerURL, "lag reconnect");
      const text = `${LAG_PROMPTS.reconnect} ${uniq()}`;
      await c5(page).watch(text);
      await send(page, text);
      await expect.poll(async () => (await c5(page).watched()).seen).toBe(true);
      // The pane's own reopen (?after=). context.setOffline doesn't drop an
      // open EventSource in Chromium (tried: no error, no new request), so
      // the browser's Last-Event-ID retry isn't reachable hermetically.
      await reattach(page, session.id);
      await expect(conversation(page).getByText(lastWord)).toBeVisible({ timeout: 20_000 });
      await expectIdle(page);
      await nextFrames(page);
      expect((await c5(page).watched()).gaps).toBe(0);
      expect(await c5(page).exactUsers(text)).toBe(1);
    });
  });

  test("no duplicates after an item settles mid-turn: reconnect, refocus, inject", async ({ page, routerURL }) => {
    test.setTimeout(60_000);
    await setConfig(routerURL, { feature_flags: { linked_sessions: true } });
    const leader = await newSession(routerURL, "dup leader");
    const subject = await openNewSession(page, routerURL, "dup");
    const linkRes = await fetch(`${routerURL}/links`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        granting_session_id: leader.id,
        subject_session_id: subject.id,
        grants: ["read", "inject"],
        cadence: "on_signal",
      }),
    });
    expect(linkRes.ok).toBeTruthy();
    const link = (await linkRes.json()) as { id: string };

    const prompt = `${DUP_PROMPT} ${uniq()}`;
    const pre = DUP_BLOCKS[0].join("");
    const post = DUP_BLOCKS[1].join("");
    await send(page, prompt);
    const tools = conversation(page).getByTestId("activity-row").filter({ hasText: DUP_TOOL });
    await expect(tools).toHaveCount(1, { timeout: 15_000 });
    // The pre-tool text is stored, and a transcript read settles it (§5).
    const stored = await fetch(`${routerURL}/sessions/${subject.id}/transcript?v=2`).then((r) => r.text());
    expect(stored).toContain(pre);

    await reattach(page, subject.id);
    await blurAndRefocus(page);
    const note = `c5 inject note ${uniq()}`;
    // Queued behind the turn; delivered as its own user_message when it ends.
    await injectLinkMessage(routerURL, link.id, note);
    // All three landed mid-tool: the tool runs 4 s.
    expect(await c5(page).count(post)).toBe(0);

    await expect(conversation(page).getByText(post)).toBeVisible({ timeout: 15_000 });
    await expect(conversation(page).getByText(note)).toBeVisible({ timeout: 15_000 });
    await expectIdle(page);
    await nextFrames(page);

    expect(await c5(page).exactUsers(prompt)).toBe(1);
    expect(await c5(page).count(pre)).toBe(1);
    expect(await c5(page).count(post)).toBe(1);
    expect(await c5(page).count(note)).toBe(1);
    // The turn's group folded at turn end; open it to count its tool.
    const group = conversation(page).getByTestId("activity-group");
    await expect(group).toHaveCount(1);
    await group.getByRole("button").first().click();
    await expect(tools).toHaveCount(1);
  });

  test("switching sessions mid-item shows the whole reply so far at once, then it continues", async ({
    page,
    routerURL,
  }) => {
    test.setTimeout(60_000);
    const a = await newSession(routerURL, "switch A");
    const b = await newSession(routerURL, "switch B");
    await page.goto(routerURL);
    await openFromRail(page, a.name);
    await sendChatMessage(routerURL, a.id, SWITCH_PROMPTS.a);
    await sendChatMessage(routerURL, b.id, SWITCH_PROMPTS.b);
    const wordsA = () => c5(page).words("swa\\d\\d");
    const wordsB = () => c5(page).words("swb\\d\\d");

    // Mid-reply in A.
    await expect.poll(async () => (await wordsA()).length, { timeout: 15_000 }).toBeGreaterThanOrEqual(5);
    const before = await wordsA();
    expect(before.length).toBeLessThan(SWITCH_WORDS.a.length);

    await openFromRail(page, b.name);
    await expect.poll(async () => (await wordsB()).length, { timeout: 15_000 }).toBeGreaterThanOrEqual(3);
    expect(await wordsA()).toEqual([]);

    // Back to A: from the click to the first frame showing at least what
    // was there before leaving, timed in the page.
    await page.evaluate((shown) => {
      const w = window as unknown as { __c5switch: { clickAt?: number; shownAt?: number } };
      w.__c5switch = {};
      document.addEventListener(
        "click",
        () => {
          w.__c5switch.clickAt = performance.now();
          const tick = () => {
            const got = (document.querySelector("[data-chat-rows]")?.textContent ?? "").match(/swa\d\d/g) ?? [];
            if (shown.every((s, i) => got[i] === s)) w.__c5switch.shownAt = performance.now();
            else requestAnimationFrame(tick);
          };
          requestAnimationFrame(tick);
        },
        { capture: true, once: true },
      );
    }, before);
    await page.locator('[aria-label="Session list"]').getByText(a.name, { exact: true }).click();
    const switched = () =>
      page.evaluate(() => (window as unknown as { __c5switch: { clickAt?: number; shownAt?: number } }).__c5switch);
    await expect.poll(async () => (await switched()).shownAt, { timeout: 5_000 }).toBeDefined();
    const { clickAt, shownAt } = await switched();
    test.info().annotations.push({ type: "switch back, ms to the reply so far", description: (shownAt! - clickAt!).toFixed(0) });
    expect(shownAt! - clickAt!, "the reply so far shows within ~300 ms of switching back").toBeLessThan(300);

    const onReturn = await wordsA();
    expect(onReturn.slice(0, before.length)).toEqual(before);
    expect(onReturn.length).toBeLessThan(SWITCH_WORDS.a.length);
    // It keeps streaming, then ends complete with nothing repeated.
    await expect.poll(async () => (await wordsA()).length).toBeGreaterThan(onReturn.length);
    await expect.poll(async () => (await wordsA()).length, { timeout: 15_000 }).toBe(SWITCH_WORDS.a.length);
    await expectIdle(page);
    await nextFrames(page);
    expect(await wordsA()).toEqual(SWITCH_WORDS.a.map((w) => w.trim()));
  });

  test("held-back store: mounting mid-tool shows the pre-tool text and the running tool once", async ({
    page,
    routerURL,
  }) => {
    test.setTimeout(60_000);
    const session = await openNewSession(page, routerURL, "held");
    const prompt = `${HELD_PROMPT} ${uniq()}`;
    const pre = HELD_BLOCKS[0].join("");
    const post = HELD_BLOCKS[1].join("");
    await send(page, prompt);
    const tools = conversation(page).getByTestId("activity-row").filter({ hasText: HELD_TOOL });
    await expect(tools).toHaveCount(1, { timeout: 15_000 });

    // Mount mid-tool, when the store has only the user's row of this turn.
    await page.reload();
    await openFromRail(page, session.name);
    await expect(conversation(page).getByText(pre)).toBeVisible();
    await expect(tools).toHaveCount(1);
    expect(await c5(page).count(pre)).toBe(1);
    expect(await c5(page).exactUsers(prompt)).toBe(1);
    expect(await c5(page).count(post)).toBe(0);

    await expect(conversation(page).getByText(post)).toBeVisible({ timeout: 15_000 });
    await expectIdle(page);
    await nextFrames(page);
    expect(await c5(page).count(pre)).toBe(1);
    expect(await c5(page).count(post)).toBe(1);
    expect(await c5(page).exactUsers(prompt)).toBe(1);
    const group = conversation(page).getByTestId("activity-group");
    await expect(group).toHaveCount(1);
    await group.getByRole("button").first().click();
    await expect(tools).toHaveCount(1);
  });

  test("a reconnect resumes from its cursor: no repeated or missing chunks", async ({ page, routerURL }) => {
    test.setTimeout(60_000);
    const session = await openNewSession(page, routerURL, "cursor");
    const opened: string[] = [];
    page.on("request", (r) => {
      if (isOutputV2(r.url(), session.id)) opened.push(r.url());
    });
    await send(page, CURSOR_PROMPT);
    const words = () => c5(page).words("cur\\d\\d");
    await expect.poll(async () => (await words()).length, { timeout: 15_000 }).toBeGreaterThanOrEqual(5);
    expect((await words()).length).toBeLessThan(CURSOR_WORDS.length);

    const reopened = await reattach(page, session.id);
    expect(new URL(reopened.url()).searchParams.get("after")).toMatch(/^[0-9a-f]{8}:\d+$/);
    // Still mid-reply when the stream came back.
    expect((await words()).length).toBeLessThan(CURSOR_WORDS.length);

    await expect.poll(async () => (await words()).length, { timeout: 15_000 }).toBe(CURSOR_WORDS.length);
    await expectIdle(page);
    await nextFrames(page);
    expect(await words()).toEqual(CURSOR_WORDS.map((w) => w.trim()));
    expect(opened.filter((u) => u.includes("after="))).toHaveLength(1);
    // A new epoch (a restarted child: hello resume "resync", then the whole
    // tail) is covered by the session service's Go tests.
  });
});
