// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// Commit and Land in the conversation (docs/SESSION-WORKTREES-DESIGN.md
// D4–D6): the header control's actions, result cards from the event log
// (replayed and live, rendered once), Undo, and "Ask the agent to resolve".
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureConfig, fixtureIsolatedSession } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope, LandResult, TranscriptMessage } from "../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: () => <textarea data-testid="fake-monaco-editor" />,
}));

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}

if (typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

const SID = fixtureIsolatedSession.id;
const transcript: TranscriptMessage[] = [
  { role: "user", text: "Fix the deploy script", timestamp: "2026-10-05T14:00:00Z" },
  { role: "agent", text: "Done — the detection works on both runners.", timestamp: "2026-10-05T14:01:00Z" },
];
const landPayload = { kind: "land", branch: "mads/session/session-isolated", base: "main", target_branch: "main" };

let nextSeq = 100;
function envelope(type: string, payload: unknown, seq = nextSeq++): Envelope {
  return { seq, type, timestamp: "2026-10-05T14:05:00Z", actor: "system", session_id: SID, payload };
}

/** Delivers one envelope on the shared control-plane bus. */
function emitControl(type: string, payload: unknown) {
  const source = FakeEventSource.instances.filter((s) => s.url.includes(type)).at(-1);
  expect(source).toBeTruthy();
  act(() => {
    source!.emit(type, JSON.stringify(envelope(type, payload)));
  });
}

function makeClient(session = fixtureIsolatedSession) {
  return new FakeRouterClient({ [SID]: session }, { [SID]: transcript });
}

async function mount(client: FakeRouterClient) {
  render(<ConversationTab sessionId={SID} routerClient={client} />);
  await screen.findByText("Done — the detection works on both runners.");
}

describe("ConversationTab — Commit and Land", () => {
  beforeEach(() => {
    nextSeq = 100;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("replays result cards from the event log, so they survive a reload", async () => {
    const client = makeClient();
    client.seedEvent(envelope("commit.created", { hash: "agent00", orphan: true }, 1));
    client.seedEvent(
      envelope("commit.created", { hash: "a41c09e5d2", source: "commit", message: "deploy: use codenames", files: 3 }, 2),
    );
    client.seedEvent(envelope("merge.completed", { ...landPayload, merge_commit: "7be2d31aa", fast_forwarded: true }, 3));
    await mount(client);
    expect(await screen.findByText("Committed 3 files")).toBeTruthy();
    expect(screen.getByText("Landed into main")).toBeTruthy();
    // The agent's own hook-reported commit draws nothing.
    expect(document.querySelectorAll("[data-worktree-card]")).toHaveLength(2);
  });

  it("opens the Commit dialog from the header when one-click commits are off", async () => {
    const client = makeClient();
    await mount(client);
    fireEvent.click(screen.getByRole("button", { name: /Commit…/ }));
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
    expect(await screen.findByText("scripts/deploy.sh")).toBeTruthy();
    expect(client.commitCalls).toHaveLength(0);
  });

  it("commits in one click with 'Write commit messages for me', shows one card, and Undo takes it back", async () => {
    const client = makeClient();
    vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, auto_commit_messages: true });
    await mount(client);
    // The settings load asynchronously; the one-click path needs both answers.
    await waitFor(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Commit…/ }));
      expect(client.commitCalls.length).toBeGreaterThan(0);
    });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(client.commitCalls[0].req).toEqual({});
    expect(await screen.findByText("Committed 4 files")).toBeTruthy();

    // Its event arrives: still one card.
    emitControl("commit.created", { hash: "a41c09e5d2", source: "commit", message: "drafted", files: 4, drafted: true });
    expect(screen.getAllByText("Committed 4 files")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(client.undoCalls).toEqual([{ sessionId: SID, hash: "a41c09e5d2" }]));
    expect(await screen.findByText(/Undone — its files are uncommitted again/)).toBeTruthy();
    emitControl("commit.undone", { hash: "a41c09e5d2" });
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("falls back to the dialog, with the reason, when a one-click Commit is refused", async () => {
    const client = makeClient();
    vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, auto_commit_messages: true });
    client.commitOutcome = { code: "git_failed", error: "git refused the commit.", detail: "Please tell me who you are." };
    await mount(client);
    await waitFor(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Commit…/ }));
      expect(client.commitCalls.length).toBeGreaterThan(0);
    });
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
    expect(screen.getByText("Please tell me who you are.")).toBeTruthy();
  });

  it("shows Landing… until Land answers, then renders the outcome once though its event also arrives", async () => {
    const client = makeClient({ ...fixtureIsolatedSession, uncommitted_count: 0 });
    let answer!: (r: LandResult) => void;
    client.landOutcome = () => new Promise<LandResult>((resolve) => (answer = resolve));
    await mount(client);

    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    expect(((await screen.findByRole("button", { name: "Landing…" })) as HTMLButtonElement).disabled).toBe(true);
    expect(client.landCalls).toEqual([SID]);

    await act(async () => {
      answer({ outcome: "refused", reason: "busy", base: "main", branch: "mads/session/session-isolated" });
    });
    expect(await screen.findByText("Can't land while the agent is working")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Land into main/ })).toBeTruthy();

    emitControl("merge.refused", { ...landPayload, reason: "busy" });
    expect(screen.getAllByText("Can't land while the agent is working")).toHaveLength(1);
  });

  it.each<[string, Record<string, unknown>, string]>([
    ["merge.completed", { merge_commit: "7be2d31aa", fast_forwarded: true }, "Landed into main"],
    ["merge.conflicted", { files: ["scripts/deploy.sh"], step: "base" }, "main moved on: 1 file conflicts"],
    ["merge.refused", { reason: "primary_dirty", files: ["scripts/deploy.sh"] }, "Your checkout has staged or uncommitted changes"],
    [
      "merge.auto_resolved",
      { files: ["scripts/deploy.sh"], merge_commit: "9f8e7d6c5b" },
      "main had moved on; mergiraf merged 1 file automatically",
    ],
  ])("draws a live %s as its card", async (type, extra, title) => {
    const client = makeClient();
    await mount(client);
    emitControl(type, { ...landPayload, ...extra });
    expect(await screen.findByText(title)).toBeTruthy();
  });

  it("'Ask the agent to resolve' drafts an editable message in the composer and sends nothing", async () => {
    const client = makeClient();
    const send = vi.spyOn(client, "sendMessage");
    client.seedEvent(envelope("merge.conflicted", { ...landPayload, files: ["scripts/deploy.sh"], step: "base" }, 5));
    await mount(client);
    fireEvent.click(await screen.findByRole("button", { name: "Ask the agent to resolve" }));
    const composer = screen.getByPlaceholderText(/message the agent/i) as HTMLTextAreaElement;
    await waitFor(() =>
      expect(composer.value).toBe(
        "Merge main into this branch and resolve the conflicts in scripts/deploy.sh, keeping both sides' changes. Then commit the merge.",
      ),
    );
    expect(send).not.toHaveBeenCalled();
  });

  it("an auto-merged Land asks the agent to check, opens the changes, and lands again", async () => {
    const client = makeClient();
    const send = vi.spyOn(client, "sendMessage");
    const onOpenCommitFile = vi.fn();
    client.seedEvent(
      envelope("merge.auto_resolved", { ...landPayload, files: ["scripts/deploy.sh"], merge_commit: "9f8e7d6c5b" }, 5),
    );
    render(<ConversationTab sessionId={SID} routerClient={client} onOpenCommitFile={onOpenCommitFile} />);
    fireEvent.click(await screen.findByRole("button", { name: "Ask the agent to check" }));
    const composer = screen.getByPlaceholderText(/message the agent/i) as HTMLTextAreaElement;
    await waitFor(() => expect(composer.value).toContain("resolved scripts/deploy.sh automatically"));
    expect(send).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "See the changes" }));
    expect(onOpenCommitFile).toHaveBeenCalledWith("9f8e7d6c5b", "scripts/deploy.sh");

    fireEvent.click(screen.getByRole("button", { name: "Land again" }));
    expect(await screen.findByText("Landed into main")).toBeTruthy();
  });
});
