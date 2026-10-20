// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: markdown goes through
// DOMPurify, which doesn't sanitize under happy-dom.
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { collapseFailedSends } from "./failedSends";
import { buildItems } from "../activity/conversationItems";
import { emptyPacer, enqueueInbound, revealedToMessages, step } from "../streaming/streamPacer";
import type { InboundSource, Link, TranscriptMessage } from "../../router/types";

const source: InboundSource = {
  channel: "peer",
  provider: "claude-code",
  from_name: "repo-9c",
  session_name: "Leader refactor",
  session_id: "s-1",
};

const message = (inbound: Partial<InboundSource> = {}, text = "please rebase onto main"): TranscriptMessage => ({
  role: "inbound",
  text,
  inbound: { ...source, ...inbound },
  timestamp: "2026-10-19T10:00:00Z",
});

describe("inbound message row", () => {
  it("names the resolved session, the harness mark and the harness, and reads as a note, not a user turn", () => {
    const { container } = render(<ChatRenderer messages={[message()]} />);
    const row = screen.getByRole("note");
    expect(row.textContent).toContain("from Leader refactor");
    expect(row.textContent).toContain("via claude-code");
    expect(row.textContent).toContain("please rebase onto main");
    expect(screen.getByRole("img", { name: "claude-code" })).toBeTruthy();
    expect(row.querySelector("time")).toBeTruthy();
    expect(row.hasAttribute("data-find")).toBe(true);
    expect(container.querySelectorAll("[data-user-msg]")).toHaveLength(0);
  });

  it("falls back to the harness's own label, then to a generic name", () => {
    const { rerender } = render(<ChatRenderer messages={[message({ session_name: undefined })]} />);
    expect(screen.getByRole("note").textContent).toContain("from repo-9c");
    rerender(<ChatRenderer messages={[message({ session_name: undefined, from_name: undefined })]} />);
    expect(screen.getByRole("note").textContent).toContain("from another session");
  });

  it("notes the relationship when the link resolves, without borrowing the link chip", () => {
    const link = { id: "l-1", granting_session_id: "s-1", subject_session_id: "me" } as Link;
    render(<ChatRenderer messages={[message({ link_id: "l-1" })]} links={[link]} viewedSessionId="me" />);
    const row = screen.getByRole("note");
    expect(row.textContent).toContain("your leader");
    expect(row.textContent).not.toContain("⇄");
  });

  it("shows a held message muted, with the reason and the body if there is one", () => {
    const { rerender } = render(
      <ChatRenderer messages={[message({ state: "held", hold_reason: "mode-mismatch" })]} />,
    );
    const row = screen.getByRole("note");
    expect(row.textContent).toContain("Message from Leader refactor held by claude-code: mode-mismatch");
    expect(row.textContent).not.toContain("via claude-code");
    rerender(<ChatRenderer messages={[message({ state: "held" }, "")]} />);
    expect(screen.getByRole("note").textContent).toContain("held by claude-code");
    expect(screen.getByRole("note").textContent).not.toContain("please rebase");
  });

  it("folds a long body behind Show more", () => {
    render(<ChatRenderer messages={[message({}, "word ".repeat(200))]} />);
    const toggle = screen.getByRole("button", { name: "Show more" });
    fireEvent.click(toggle);
    expect(screen.getByRole("button", { name: "Show less" })).toBeTruthy();
  });

  it("renders a live inbound item and the pacer hands it to history as a transcript row", () => {
    let s = enqueueInbound(emptyPacer(), "ping", source, "2026-10-19T10:00:00Z");
    s = step(s, 1000);
    expect(s.items.map((i) => i.kind)).toEqual(["inbound"]);
    render(<ChatRenderer messages={[]} liveItems={s.items} />);
    expect(screen.getByRole("note").textContent).toContain("from Leader refactor");
    expect(revealedToMessages(s.items)).toEqual([
      { role: "inbound", text: "ping", inbound: source, timestamp: "2026-10-19T10:00:00Z" },
    ]);
  });

  it("is a message item keyed by position, so the live row and the persisted row share a slot", () => {
    const live = buildItems([{ type: "message", message: message(), live: true }]);
    const persisted = buildItems([{ type: "message", message: message() }]);
    expect(live[0].key).toBe(persisted[0].key);
  });
});

describe("inbound and failed sends", () => {
  it("is not the latest send: a failed user send keeps its Retry and an inbound never offers one", () => {
    const sent: TranscriptMessage = { role: "user", text: "do it" };
    const err: TranscriptMessage = { role: "agent", text: "auth rejected", is_error: true };
    const { delivery } = collapseFailedSends([sent, err, message()]);
    expect(delivery.get(sent)?.error).toBe("auth rejected");

    // An inbound after the send opens its own exchange rather than counting as a reply.
    const out = collapseFailedSends([sent, message(), { role: "agent", text: "ok" }]);
    expect(out.delivery.size).toBe(0);
  });

  it("does not retry the inbound as a user send", () => {
    const retried: string[] = [];
    render(<ChatRenderer messages={[message()]} onRetry={(t) => retried.push(t)} />);
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
    expect(retried).toEqual([]);
  });
});
