import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import type { Envelope, SessionReport } from "../../../router/types";
import {
  FakeRouterClient,
  fixtureSessionReportApiKey,
  fixtureSessionReportLocal,
  fixtureSessionReportPlan,
} from "../../../router/fixtures";
import { findCommand } from "./registry";
import type { CommandApi, CommandContext } from "./types";
import { usageCommand } from "./usage";
import { Composer } from "../Composer";

// The card rides the shared control-plane bus; capture its subscriber so a
// test can deliver envelopes without an EventSource.
const subs: { sessionId?: string; types: string[]; onEvent: (e: Envelope) => void }[] = [];
vi.mock("../../../router/events", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  subscribeToEvents: (
    params: { sessionId?: string; types: string[] },
    onEvent: (e: Envelope) => void,
  ) => {
    const sub = { ...params, onEvent };
    subs.push(sub);
    return () => subs.splice(subs.indexOf(sub), 1);
  },
}));

beforeEach(() => {
  subs.length = 0;
});
afterEach(() => vi.restoreAllMocks());

function open(
  sessionId: string,
  opts: { compact?: () => void; ctx?: Partial<CommandContext> } = {},
) {
  const client = new FakeRouterClient();
  let card: ((close: () => void) => ReactNode) | undefined;
  const api = {
    ctx: { routerClient: client, sessionId, ...opts.ctx },
    notify: vi.fn(),
    fail: vi.fn(),
    openCard: (r: (close: () => void) => ReactNode) => (card = r),
    compact: opts.compact,
  } as unknown as CommandApi;
  void usageCommand.execute({ args: "" }, api);
  if (!card) throw new Error("/usage opened no card");
  render(<>{card(vi.fn())}</>);
  return { client };
}

const sections = () =>
  screen.getAllByRole("heading").map((h) => h.textContent);

async function loaded() {
  await screen.findByText("Health");
}

describe("/usage", () => {
  it("is reached through its aliases", () => {
    const ctx = { routerClient: new FakeRouterClient(), sessionId: "s" };
    expect(findCommand("status", ctx)).toBe(usageCommand);
    expect(findCommand("context", ctx)).toBe(usageCommand);
    expect(findCommand("usage", ctx)).toBe(usageCommand);
  });

  it("renders a plan session: limits, cache, delegates, Compact now", async () => {
    open(fixtureSessionReportPlan.session_id, { compact: vi.fn() });
    await loaded();
    expect(sections()).toEqual(["Health", "Context", "Tokens · session", "Plan limits"]);
    const card = screen.getByTestId("usage-card");
    expect(within(card).getByText(/claude-code 2\.1\.290/)).toBeTruthy();
    expect(within(card).getByLabelText("preflight ok")).toBeTruthy();
    expect(within(card).getByText("142.3k / 200k")).toBeTruthy();
    expect(within(card).getByText("Auto-compact at 85%")).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Compact now" })).toBeTruthy();
    // 228k fresh + 982k read + 31k written.
    expect(within(card).getByText("1.2M / 48.6k")).toBeTruthy();
    expect(within(card).getByText("982k (79%)")).toBeTruthy();
    expect(within(card).getByText("↳ delegates")).toBeTruthy();
    expect(within(card).getByText("37")).toBeTruthy();
    expect(within(card).getByRole("meter", { name: "5-hour limit" }).getAttribute("aria-valuenow")).toBe("62");
    expect(within(card).getByText(/^62% · resets /)).toBeTruthy();
    expect(within(card).getByText(/^31% · resets /)).toBeTruthy();
    expect(within(card).queryByText(/Spend/)).toBeNull();
    expect(card.textContent).not.toMatch(/Compress/);
  });

  it("renders an API-key session: spend, no limits, no delegates", async () => {
    open(fixtureSessionReportApiKey.session_id, { compact: vi.fn() });
    await loaded();
    expect(sections()).toEqual(["Health", "Context", "Tokens · session", "Spend · session"]);
    expect(screen.getByText("≈ Cost (list price)")).toBeTruthy();
    expect(screen.getByText("$0.91")).toBeTruthy();
    expect(screen.queryByText("↳ delegates")).toBeNull();
    expect(screen.getByText("241k (78%)")).toBeTruthy();
  });

  it("renders a local session: no cache, no spend, no Compact", async () => {
    open(fixtureSessionReportLocal.session_id, { compact: vi.fn() });
    await loaded();
    expect(sections()).toEqual(["Health", "Context", "Tokens · session"]);
    expect(screen.getByText(/goose 1\.31\.0/)).toBeTruthy();
    expect(screen.queryByText("From cache")).toBeNull();
    expect(screen.queryByText(/Auto-compact/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Compact now" })).toBeNull();
  });

  it("shows only Health for a session that never finished a turn", async () => {
    open("session-inactive", { compact: vi.fn() });
    await loaded();
    expect(sections()).toEqual(["Health"]);
    expect(screen.getByText(/sleeping/)).toBeTruthy();
    expect(screen.queryByText("Process · last event")).toBeNull();
  });

  it("Compact now runs the host's compact path", async () => {
    const compact = vi.fn();
    open(fixtureSessionReportPlan.session_id, { compact });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Compact now" }));
    expect(compact).toHaveBeenCalledWith();
  });

  it("no Compact link when the host can't compact, or the provider can't", async () => {
    open(fixtureSessionReportPlan.session_id);
    await loaded();
    expect(screen.getByText("Auto-compact at 85%")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Compact now" })).toBeNull();
  });

  it("no Compact link when the provider matrix says it doesn't compact", async () => {
    open(fixtureSessionReportPlan.session_id, {
      compact: vi.fn(),
      ctx: { compaction: { compacts: false, takesFocus: false } },
    });
    await loaded();
    expect(screen.queryByRole("button", { name: "Compact now" })).toBeNull();
  });

  it("refetches on the session's turn, agent-state, compact and usage events", async () => {
    const { client } = open(fixtureSessionReportPlan.session_id, { compact: vi.fn() });
    await loaded();
    // This session's events, plus every session's usage (the next test).
    expect(subs).toHaveLength(2);
    const own = subs.find((sub) => sub.sessionId)!;
    expect(own.sessionId).toBe(fixtureSessionReportPlan.session_id);
    expect(own.types).toEqual(
      expect.arrayContaining([
        "session.turn_ended",
        "session.agent_state",
        "session.compact",
        "session.usage_updated",
      ]),
    );

    const next: SessionReport = structuredClone(fixtureSessionReportPlan);
    next.tokens!.turns = 38;
    next.context!.used = 150_000;
    client.sessionReports.set(next.session_id, next);
    act(() =>
      own.onEvent({ seq: 1, timestamp: "", type: "session.turn_ended", actor: "t" }),
    );
    await screen.findByText("38");
    expect(screen.getByText("150k / 200k")).toBeTruthy();

    // The turn's usage lands after turn_ended: usage_updated refetches it.
    const landed: SessionReport = structuredClone(next);
    landed.tokens!.turns = 39;
    client.sessionReports.set(landed.session_id, landed);
    act(() =>
      own.onEvent({ seq: 10, timestamp: "", type: "session.usage_updated", actor: "t" }),
    );
    await screen.findByText("39");

    act(() =>
      own.onEvent({
        seq: 2, timestamp: "", type: "session.compact", actor: "t",
        payload: { phase: "start" },
      }),
    );
    await screen.findByText("Compacting…");
    expect(screen.queryByRole("button", { name: "Compact now" })).toBeNull();
    act(() =>
      own.onEvent({
        seq: 3, timestamp: "", type: "session.compact", actor: "t",
        payload: { phase: "done", success: true },
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Compact now" })).toBeTruthy(),
    );
  });
});

describe("/usage in the composer", () => {
  it.each(["/usage", "/status", "/context"])(
    "%s + Enter opens the Ledger above the composer",
    async (typed) => {
      const client = new FakeRouterClient();
      const send = vi.spyOn(client, "sendMessage");
      render(<Composer routerClient={client} sessionId="session-active" />);
      const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
      fireEvent.change(textarea, { target: { value: typed } });
      fireEvent.keyDown(textarea, { key: "Enter" });
      const host = await screen.findByTestId("command-card");
      await within(host).findByText("Plan limits");
      expect(within(host).getByTestId("usage-card")).toBeTruthy();
      expect(send).not.toHaveBeenCalled();
      expect(textarea.value).toBe("");
    },
  );

  it("plan limits are the login's: another session's reading refetches, and an old one says its age", async () => {
    const now = Date.parse("2026-10-21T15:00:00Z");
    vi.spyOn(Date, "now").mockReturnValue(now);
    const { client } = open(fixtureSessionReportPlan.session_id);
    await loaded();
    expect(sections()).toContain("Plan limits");

    const anySession = subs.find((sub) => !sub.sessionId)!;
    expect(anySession.types).toEqual(["session.usage_updated"]);
    const old: SessionReport = structuredClone(fixtureSessionReportPlan);
    old.limits_at = new Date(now - (3 * 60 + 20) * 60_000).toISOString();
    client.sessionReports.set(old.session_id, old);
    act(() =>
      anySession.onEvent({ seq: 5, timestamp: "", type: "session.usage_updated", actor: "t", session_id: "ses_other" }),
    );
    await waitFor(() => expect(sections()).toContain("Plan limits · as of 3h 20m ago"));
  });

  it("listens for other sessions' readings only on a plan", async () => {
    open(fixtureSessionReportApiKey.session_id);
    await loaded();
    expect(subs.filter((sub) => !sub.sessionId)).toHaveLength(0);
  });
});
