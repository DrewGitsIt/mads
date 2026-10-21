import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { UNDELIVERED } from "./failedSends";
import { FLAGS } from "../../featureFlags";
import type { Link, PermissionRequest, TranscriptMessage } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";

/** Transcript messages as keyed rows: positional keys, and a tool_result
 * folded onto its call's row (`tool.result`), as keyPage does. */
function keyed(ms: TranscriptMessage[]): KeyedRow[] {
  const out: KeyedRow[] = [];
  ms.forEach((m, i) => {
    if (m.role === "tool_result") {
      const call = out.find((r) => r.role === "tool" && r.tool?.id === m.tool?.id);
      if (call?.tool) call.tool = { ...call.tool, result: { text: m.text, is_error: m.is_error, timestamp: m.timestamp } };
      return;
    }
    out.push({ ...m, key: m.key ?? `r${i}` });
  });
  return out;
}

/** useLinkNames for makeLink's lnk_1: its subject is "payments refactor". */
const paymentsName = (id: string) => (id === "lnk_1" ? "payments refactor" : undefined);

function makeLink(overrides: Partial<Link>): Link {
  return {
    id: "lnk_1",
    granting_session_id: "leader-id",
    subject_session_id: "subject-id",
    grants: ["read", "inject", "orchestrate"],
    cadence: "per_turn",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("ChatRenderer", () => {
  afterEach(() => {
    FLAGS.linkedSessions = false;
  });

  it("renders user and agent prose through the markdown renderer", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "hello **there**" },
      { role: "agent", text: "hi back" },
    ];
    render(<ChatRenderer rows={keyed(messages)} />);
    expect(screen.getByText("there").tagName).toBe("STRONG");
    expect(screen.getByText("hi back")).toBeTruthy();
  });

  // A Shift+Enter in the composer is a new line in the history too; an
  // agent's single newline stays markdown's soft wrap.
  it("breaks a user message's lines where they were typed, but not an agent's", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "first line\nsecond line" },
      { role: "agent", text: "wrapped\nprose" },
    ];
    const { container } = render(<ChatRenderer rows={keyed(messages)} />);
    const user = container.querySelector("[data-user-msg]")!;
    expect(user.querySelectorAll("br")).toHaveLength(1);
    expect(screen.getByText(/wrapped/).querySelector("br")).toBeNull();
  });

  it("shows a user message's attached files as thumbnails and tiles, not the path list", () => {
    const text =
      "why is this red?\n\nAttached files (open each one with your file-reading tool):\n" +
      "- /Users/me/.mads/attachments/s1/a1/screenshot.png\n" +
      "- /Users/me/.mads/attachments/s1/b2/build.log";
    const url = (id: string, variant?: string) =>
      `http://router/sessions/s1/attachments/${id}${variant ? `/${variant}` : ""}`;
    render(<ChatRenderer rows={keyed([{ role: "user", text }])} attachmentUrl={url} />);
    expect(screen.getByText("why is this red?")).toBeTruthy();
    expect(screen.queryByText(/Attached files/)).toBeNull();
    const thumb = screen.getByAltText("screenshot.png");
    expect(thumb.getAttribute("src")).toBe(url("a1", "thumbnail"));
    expect(thumb.closest("a")?.getAttribute("href")).toBe(url("a1"));
    expect(screen.getByText("build.log").closest("a")?.getAttribute("href")).toBe(url("b2"));
  });

  it("falls back to a named tile when an image has no thumbnail", () => {
    const text =
      "Attached files (open each one with your file-reading tool):\n" +
      "- /h/.mads/attachments/s1/a1/huge.png";
    render(
      <ChatRenderer rows={keyed([{ role: "user", text }])} attachmentUrl={(id) => `http://r/${id}`} />,
    );
    fireEvent.error(screen.getByAltText("huge.png"));
    expect(screen.getByText("huge.png")).toBeTruthy();
    expect(screen.queryByAltText("huge.png")).toBeNull();
  });

  it("renders tool messages as a collapsed activity group that expands to rows", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool", tool: { name: "Read", path: "a.go" } },
    ];
    render(<ChatRenderer rows={keyed(messages)} />);
    const header = screen.getByRole("button", { name: /1 action · 1 read/ });
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("activity-row")).toBeNull();

    fireEvent.click(header);
    expect(header.getAttribute("aria-expanded")).toBe("true");
    // The row title is the server title (absent here) falling back to the
    // legacy summarize() — the path for a Read.
    expect(screen.getByText("a.go")).toBeTruthy();
  });

  it("folds a tool_result into its tool_use's row — one row per call, zero standalone result cards", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "read the image and run the tests" },
      {
        role: "tool",
        tool: { id: "toolu_11", name: "Read", path: "docs/screenshot.png" },
        timestamp: "2026-07-19T10:00:01Z",
      },
      {
        role: "tool_result",
        text: "permission denied: image reads require approval",
        tool: { id: "toolu_11", name: "" },
        is_error: true,
        timestamp: "2026-07-19T10:00:02Z",
      },
      { role: "agent", text: "I can't read that image without approval." },
      {
        role: "tool",
        tool: { id: "toolu_12", name: "Bash", input: { command: "go test ./..." } },
        timestamp: "2026-07-19T10:00:04Z",
      },
      {
        role: "tool_result",
        text: "ok",
        tool: { id: "toolu_12", name: "" },
        timestamp: "2026-07-19T10:00:09Z",
      },
    ];
    render(<ChatRenderer rows={keyed(messages)} />);

    // Two activity groups (split by the agent's prose), each carrying its
    // one call's outcome — never a standalone result card.
    const groups = screen.getAllByTestId("activity-group");
    expect(groups).toHaveLength(2);
    expect(screen.queryByText("no input")).toBeNull();

    const [readGroup, bashGroup] = groups;
    // Aggregate status glyphs on the collapsed headers: the failed Read
    // renders ✕, the ok Bash ✓ with its duration.
    expect(readGroup.querySelector('[data-status="failed"]')).toBeTruthy();
    expect(bashGroup.querySelector('[data-status="completed"]')).toBeTruthy();
    expect(bashGroup.textContent).toContain("5.0s");
  });

  it("with the flag off, ignores attribution entirely and renders the plain role bubble", () => {
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "do the thing",
        attribution: { source_name: "Leader Session", kind: "message", relation: "your leader" },
      },
    ];
    const { container } = render(
      <ChatRenderer rows={keyed(messages)} links={[makeLink({})]} viewedSessionId="subject-id" />,
    );
    expect(screen.queryByText(/Leader Session/)).toBeNull();
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/_user_/);
    expect(wrapper?.className).not.toMatch(/injected/);
  });

  it("joins adjacent agent text into one bubble, ended by a tool call", () => {
    const { container } = render(
      <ChatRenderer
        rows={keyed([
          { role: "agent", text: "first part" },
          { role: "thinking", text: "" },
          { role: "agent", text: "second part" },
          { role: "tool", tool: { id: "t1", name: "Read", path: "a.go" } },
          { role: "agent", text: "after the call" },
        ])}
      />,
    );
    // Empty thinking is hidden and doesn't split the bubble.
    const bubbles = container.querySelectorAll('[class*="_agent_"]');
    expect(bubbles).toHaveLength(2);
    expect(bubbles[0].textContent).toContain("first part");
    expect(bubbles[0].textContent).toContain("second part");
    expect(screen.getAllByTestId("activity-group")).toHaveLength(1);
  });

  it("renders stored thinking with text in place, as an activity row", () => {
    render(
      <ChatRenderer
        rows={keyed([
          { role: "user", text: "why?" },
          { role: "thinking", text: "weighing the options" },
          { role: "agent", text: "because" },
        ])}
      />,
    );
    const group = screen.getByTestId("activity-group");
    fireEvent.click(within(group).getAllByRole("button")[0]);
    expect(group.textContent).toContain("weighing the options");
    expect(group.compareDocumentPosition(screen.getByText("because")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("holds the current turn's activity open while the turn is active, and folds it after", () => {
    vi.useFakeTimers();
    const current: KeyedRow[] = [
      { key: "u1", role: "user", text: "go" },
      { key: "k1", role: "tool", tool: { id: "t1", name: "Read", path: "a.go" }, current: true, live: true },
      { key: "k2", role: "agent", text: "reading", current: true, live: true },
    ];
    const { rerender } = render(<ChatRenderer rows={current} turnActive />);
    const header = () => within(screen.getByTestId("activity-group")).getAllByRole("button")[0];
    expect(header().getAttribute("aria-expanded")).toBe("true");
    // The stored copy replaces the live row under the same key: same group, now folding.
    rerender(<ChatRenderer rows={current.map((r) => ({ ...r, live: false }))} />);
    act(() => {
      vi.runAllTimers();
    });
    expect(header().getAttribute("aria-expanded")).toBe("false");
    vi.useRealTimers();
  });

  it("renders an agent-written notice as a quiet system line, not the agent's voice", () => {
    const { container } = render(
      <ChatRenderer rows={keyed([{ role: "agent", text: "Interrupted by the user", notice: true }])} />,
    );
    const line = screen.getByRole("note");
    expect(line.textContent).toContain("Interrupted by the user");
    expect(line.className).toMatch(/notification/);
    expect(container.querySelector('[class*="_agent_"]')).toBeNull();
  });
});

describe("ChatRenderer — a notice from mads itself", () => {
  it("renders an attributed message with no link as a notice, never the user's bubble, flag or not", () => {
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "A pipeline task merged into your folder while you were working",
        attribution: { source_name: "your pipeline run", kind: "notice", relation: "your pipeline run" },
      },
    ];
    const { container } = render(<ChatRenderer rows={keyed(messages)} />);
    expect(screen.getByText(/from your pipeline run/)).toBeTruthy();
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/injected/);
    expect(wrapper?.className).not.toMatch(/_user_/);
  });
});

describe("ChatRenderer — linked sessions (flag-gated)", () => {
  afterEach(() => {
    FLAGS.linkedSessions = false;
  });

  it("renders a message as an inject chip naming the sender by relation, never the user's own bubble", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "do the thing",
        attribution: { source_name: "Leader Session", kind: "message", relation: "your leader" },
      },
    ];
    const { container } = render(
      <ChatRenderer rows={keyed(messages)} links={[makeLink({})]} viewedSessionId="subject-id" />,
    );
    expect(screen.getByText("⇄ from your leader · Leader Session")).toBeTruthy();
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/injected/);
    expect(wrapper?.className).not.toMatch(/\buser\b/);
  });

  it("renders a digest card, with the cadence of the link its sender is named on", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "compressed summary of the turn",
        attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
      },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "per_turn" })]}
        viewedSessionId="leader-id"
        linkName={paymentsName}
      />,
    );
    expect(screen.getByText(/payments refactor/)).toBeTruthy();
    expect(screen.getByText("compressed summary of the turn")).toBeTruthy();
    expect(screen.getByText("cadence: digest after each burst of activity")).toBeTruthy();
  });

  it("renders honest manual-only cadence copy — no automatic digests promised", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "compressed summary of the turn",
        attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
      },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "manual_only" })]}
        viewedSessionId="leader-id"
        linkName={paymentsName}
      />,
    );
    expect(screen.getByText("cadence: manual — no automatic digests")).toBeTruthy();
  });

  it("still renders a digest card when no link matches its sender — just without a cadence line", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "summary from a session since unlinked",
        attribution: { source_name: "old worker", kind: "digest", relation: "a session you monitor" },
      },
    ];
    const { container } = render(
      <ChatRenderer rows={keyed(messages)} links={[]} viewedSessionId="leader-id" linkName={paymentsName} />,
    );
    expect(screen.getByText("summary from a session since unlinked")).toBeTruthy();
    expect(container.querySelector('[class*="digestCadence"]')).toBeNull();
    expect(container.querySelector('[class*="digest"]')).toBeTruthy();
  });

  it("renders a signal as a digest card and collapses its NOOP the same way", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "Task done: wire the adapter",
        attribution: { source_name: "your pipeline run", kind: "signal", relation: "your pipeline run" },
      },
      { role: "agent", text: "NOOP" },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ id: "lnk_self", granting_session_id: "leader-id", subject_session_id: "leader-id", implicit: true, cadence: "on_signal" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/1 update from your pipeline run · no action needed/)).toBeTruthy();
    expect(screen.queryByText("NOOP")).toBeNull();
  });

  it("folds a coalesced digest run answered by one NOOP into a single chip", () => {
    FLAGS.linkedSessions = true;
    const digest = (text: string): TranscriptMessage => ({
      role: "user",
      text,
      attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
    });
    render(
      <ChatRenderer
        rows={keyed([digest("first of the run"), digest("second of the run"), { role: "agent", text: "NOOP" }])}
        links={[makeLink({})]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/2 updates from payments refactor · no action needed/)).toBeTruthy();
    expect(screen.queryByText("first of the run")).toBeNull();
    expect(screen.queryByText("NOOP")).toBeNull();
  });

  it("never folds a notice into a quiet check: a NOOP answering it reads as acknowledged", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "You can now direct payments refactor.",
        attribution: { source_name: "mads", kind: "notice", digest_attributed: true },
      },
      { role: "agent", text: "NOOP" },
    ];
    render(<ChatRenderer rows={keyed(messages)} links={[makeLink({})]} viewedSessionId="leader-id" />);
    expect(screen.queryByText(/updates? from/)).toBeNull();
    // Collapsed by default, with a title that says what it is.
    expect(screen.queryByText("You can now direct payments refactor.")).toBeNull();
    const row = screen.getByRole("button", { name: /Notice from mads/ });
    expect(row.textContent).toContain("[System] Notice from mads · expand to show message");
    fireEvent.click(row);
    expect(screen.getByText("You can now direct payments refactor.")).toBeTruthy();
    expect(screen.getByText("acknowledged")).toBeTruthy();
    expect(screen.queryByText("NOOP")).toBeNull();
  });

  it("collapses a digest answered with exactly NOOP into a quiet-check chip", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "routine progress digest",
        attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
      },
      { role: "agent", text: "NOOP" },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "on_signal" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/1 update from payments refactor · no action needed/)).toBeTruthy();
    expect(screen.queryByText("routine progress digest")).toBeNull();
    expect(screen.queryByText("NOOP")).toBeNull();
  });

  it("aggregates consecutive quiet checks into one chip, and leaves a real reply alone", () => {
    FLAGS.linkedSessions = true;
    const digest = (text: string): TranscriptMessage => ({
      role: "user",
      text,
      attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
    });
    const messages: TranscriptMessage[] = [
      digest("digest one"),
      { role: "agent", text: "NOOP" },
      digest("digest two"),
      { role: "agent", text: "NOOP" },
      digest("digest three"),
      { role: "agent", text: "I'll step in and unblock the task." },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "on_signal" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/2 updates from payments refactor · no action needed/)).toBeTruthy();
    // The third digest got a real reply — both render in full.
    expect(screen.getByText("digest three")).toBeTruthy();
    expect(screen.getByText("I'll step in and unblock the task.")).toBeTruthy();
  });

  it("collapses past the monitor's thinking, but not past a tool call", () => {
    FLAGS.linkedSessions = true;
    const digest = (text: string, at: string): TranscriptMessage => ({
      role: "user",
      text,
      timestamp: at,
      attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
    });
    const messages: TranscriptMessage[] = [
      digest("thought then noop", "2026-10-15T10:00:00Z"),
      // Stored thinking arrives in place, as its own row.
      { role: "thinking", text: "nothing here needs me", timestamp: "2026-10-15T10:00:01Z" },
      { role: "agent", text: "NOOP", timestamp: "2026-10-15T10:00:02Z" },
      digest("worked then noop", "2026-10-15T10:01:00Z"),
      {
        role: "tool",
        timestamp: "2026-10-15T10:01:01Z",
        tool: { id: "t1", name: "Read", kind: "read", title: "read notes.md" },
      },
      { role: "agent", text: "NOOP", timestamp: "2026-10-15T10:01:02Z" },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "per_turn" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/1 update from payments refactor · no action needed/)).toBeTruthy();
    expect(screen.queryByText("thought then noop")).toBeNull();
    // The thinking went into the chip with its NOOP: no group of its own.
    expect(screen.getAllByTestId("activity-group")).toHaveLength(1);
    // A tool call means the monitor did real work: the pair stays visible.
    expect(screen.getByText("worked then noop")).toBeTruthy();
    expect(screen.getByText("NOOP")).toBeTruthy();
  });

  it("expands a quiet-check chip to show the digests it absorbed", () => {
    FLAGS.linkedSessions = true;
    const digest = (text: string): TranscriptMessage => ({
      role: "user",
      text,
      attribution: { source_name: "payments refactor", kind: "digest", relation: "a session you lead" },
    });
    const messages: TranscriptMessage[] = [
      digest("digest one"),
      { role: "agent", text: "NOOP" },
      digest("digest two"),
      { role: "agent", text: "NOOP" },
    ];
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({ cadence: "per_turn" })]}
        viewedSessionId="leader-id"
      />,
    );
    const chip = screen.getByRole("button", { name: /2 updates from payments refactor/ });
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByText("digest one")).toBeNull();
    fireEvent.click(chip);
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText("digest one")).toBeTruthy();
    expect(screen.getByText("digest two")).toBeTruthy();
    expect(screen.queryByText("NOOP")).toBeNull();
    fireEvent.click(chip);
    expect(screen.queryByText("digest one")).toBeNull();
  });

  it("never collapses a NOOP-shaped reply to the subject's own inject chip", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "kick off the refactor",
        attribution: { source_name: "Leader Session", kind: "message", relation: "your leader" },
      },
      { role: "agent", text: "NOOP" },
    ];
    // A linked session's message is an inject chip, not a digest card —
    // the reply stays visible.
    render(
      <ChatRenderer
        rows={keyed(messages)}
        links={[makeLink({})]}
        viewedSessionId="subject-id"
      />,
    );
    expect(screen.getByText("kick off the refactor")).toBeTruthy();
    expect(screen.getByText("NOOP")).toBeTruthy();
  });
});

describe("ChatRenderer — permission card placement", () => {
  const transcript: TranscriptMessage[] = [
    { role: "user", text: "tidy up" },
    { role: "tool", tool: { id: "t1", name: "Bash", kind: "execute", title: "ls /etc" } },
    { role: "tool", tool: { id: "t2", name: "Read", kind: "read", title: "read /etc/hosts" } },
    {
      role: "tool",
      tool: { id: "tu1", name: "Agent", kind: "agent", input: { description: "Audit config" } },
    },
  ];
  const req = (over: Partial<PermissionRequest>): PermissionRequest => ({
    id: "perm-1",
    tool: "Read",
    status: "pending",
    ...over,
  });
  const cardsIn = (el: HTMLElement) => within(el).queryAllByRole("button", { name: "Allow" }).length;

  it("attaches a card with tool_use_id right under that call's row", () => {
    render(
      <ChatRenderer
        rows={keyed(transcript)}
        permissionRequests={[req({ tool_use_id: "t2", reason: "Path is outside allowed working directories" })]}
      />,
    );
    const group = screen.getByTestId("activity-group");
    // The pending request opened the (historical, so otherwise collapsed) group.
    const rows = screen.getAllByTestId("activity-row");
    expect(rows[1].nextElementSibling!.textContent).toContain("awaiting your decision");
    expect(group.textContent).toContain("Path is outside allowed working directories");
    expect(cardsIn(screen.getByTestId("task-card"))).toBe(0);
  });

  it("attaches a subagent call's card to its spawning task card", () => {
    render(
      <ChatRenderer
        rows={keyed(transcript)}
        permissionRequests={[req({ tool: "Bash", tool_use_id: "sub1", parent_tool_use_id: "tu1" })]}
      />,
    );
    expect(cardsIn(screen.getByTestId("task-card"))).toBe(1);
    expect(cardsIn(screen.getByTestId("activity-group"))).toBe(0);
  });

  it("falls back to the trailing list without a tool_use_id, or when the call isn't rendered yet", () => {
    const { container } = render(
      <ChatRenderer
        rows={keyed(transcript)}
        permissionRequests={[
          req({ id: "perm-a" }),
          req({ id: "perm-b", tool_use_id: "not-yet" }),
          req({ id: "perm-c", tool_use_id: "sub9", parent_tool_use_id: "tu-unknown" }),
        ]}
      />,
    );
    const stream = container.firstElementChild!;
    expect(cardsIn(screen.getByTestId("activity-group"))).toBe(0);
    expect(cardsIn(screen.getByTestId("task-card"))).toBe(0);
    // All three trail the stream, after the last item (the task card).
    const trailing = [...stream.children].slice(-3);
    expect(trailing.every((el) => el.textContent?.includes("awaiting your decision"))).toBe(true);
  });
});

describe("ChatRenderer — sends the harness failed", () => {
  const failed: TranscriptMessage[] = [
    { role: "user", text: "deploy it" },
    { role: "agent", text: "Login expired · Please run /login", is_error: true },
  ];

  it("shows the message as not delivered, with the reason in place of the error reply", () => {
    render(<ChatRenderer rows={keyed(failed)} />);
    const note = screen.getByRole("status");
    expect(note.textContent).toContain("Not delivered");
    expect(note.textContent).toContain("Login expired · Please run /login");
    // The error is no longer an agent bubble of its own.
    expect(screen.getAllByText("Login expired · Please run /login")).toHaveLength(1);
  });

  it("offers Retry on the latest message between turns, resending its text", () => {
    const onRetry = vi.fn();
    const { rerender } = render(<ChatRenderer rows={keyed(failed)} onRetry={onRetry} />);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledWith("deploy it");

    rerender(<ChatRenderer rows={keyed(failed)} onRetry={onRetry} turnActive />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();

    rerender(<ChatRenderer rows={keyed([...failed, { role: "user", text: "something else" }])} onRetry={onRetry} />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("collapses a successful retry to one message with an expandable failed-attempt count", () => {
    render(
      <ChatRenderer
        rows={keyed([...failed, { role: "user", text: "deploy it" }, { role: "agent", text: "deployed" }])}
      />,
    );
    expect(screen.getAllByText("deploy it")).toHaveLength(1);
    expect(screen.queryByText("Not delivered")).toBeNull();
    const count = screen.getByRole("button", { name: "1 failed attempt" });
    expect(screen.queryByText("Login expired · Please run /login")).toBeNull();
    fireEvent.click(count);
    expect(screen.getByText("Login expired · Please run /login")).toBeTruthy();
  });

  it("shows a send the stream reported unsaved as not delivered, with the send's failure", () => {
    const rows: KeyedRow[] = [{ key: "u1", role: "user", text: "ship it", failed: true }];
    const { rerender } = render(<ChatRenderer rows={rows} sendFailure="HTTP 503: router unavailable" />);
    expect(screen.getByRole("status").textContent).toContain("HTTP 503: router unavailable");
    rerender(<ChatRenderer rows={rows} />);
    expect(screen.getByRole("status").textContent).toContain(UNDELIVERED);
  });
});

describe("ChatRenderer — quiet-update pills", () => {
  const upd = (source: string, text: string, kind: "digest" | "signal" = "digest", timestamp?: string): TranscriptMessage[] => [
    { role: "user", text, timestamp, attribution: { source_name: source, kind, relation: "a session you lead" } },
    { role: "agent", text: "NOOP" },
  ];
  const renderPill = (messages: TranscriptMessage[]) => {
    FLAGS.linkedSessions = true;
    return render(<ChatRenderer rows={keyed(messages)} links={[]} viewedSessionId="leader-id" />);
  };

  it("names one source, singular and plural", () => {
    renderPill(upd("Pipeline Design Study", "a"));
    expect(screen.getByText(/^▸ 1 update from Pipeline Design Study · no action needed$/)).toBeTruthy();
  });

  it("names the source for several updates", () => {
    renderPill([...upd("MADS Pipeline Design Study", "a"), ...upd("MADS Pipeline Design Study", "b"), ...upd("MADS Pipeline Design Study", "c"), ...upd("MADS Pipeline Design Study", "d")]);
    expect(screen.getByText(/4 updates from MADS Pipeline Design Study · no action needed/)).toBeTruthy();
  });

  it("names two sources, and counts three or more", () => {
    const { unmount } = renderPill([...upd("Pipeline Design Study", "a"), ...upd("Classifier Primer", "b"), ...upd("Pipeline Design Study", "c")]);
    expect(screen.getByText(/3 updates from Pipeline Design Study and Classifier Primer · no action needed/)).toBeTruthy();
    unmount();
    renderPill([...upd("A", "a"), ...upd("B", "b"), ...upd("C", "c"), ...upd("A", "d")]);
    expect(screen.getByText(/4 updates from 3 sessions · no action needed/)).toBeTruthy();
  });

  it("expands grouped by session, oldest first, with kind and time", () => {
    renderPill([
      ...upd("Pipeline Design Study", "p one", "digest", "2026-10-15T10:00:00Z"),
      ...upd("Classifier Primer", "c one", "signal"),
      ...upd("Pipeline Design Study", "p two"),
    ]);
    fireEvent.click(screen.getByRole("button", { name: /3 updates/ }));
    expect(screen.getByText("Pipeline Design Study ×2 · Classifier Primer ×1")).toBeTruthy();
    const texts = ["p one", "p two", "c one"].map((t) => screen.getByText(t));
    // Document order: both Pipeline updates, then the Classifier one.
    expect(texts[0].compareDocumentPosition(texts[1]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(texts[1].compareDocumentPosition(texts[2]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText(/⌗ signal/)).toBeTruthy();
    expect(screen.getAllByText(/⌗ digest/)[0].textContent).toMatch(/·.*\d/);
  });

  it("keeps a notice out of the pill", () => {
    renderPill([
      ...upd("Pipeline Design Study", "a"),
      { role: "user", text: "A notice.", attribution: { source_name: "mads", kind: "notice" } },
      { role: "agent", text: "NOOP" },
      ...upd("Pipeline Design Study", "b"),
    ]);
    expect(screen.getAllByText(/1 update from Pipeline Design Study/)).toHaveLength(2);
    expect(screen.getByText("acknowledged")).toBeTruthy();
  });

  it("titles the grant notice by what it gives", () => {
    FLAGS.linkedSessions = true;
    render(
      <ChatRenderer
        rows={keyed([{
          role: "user",
          text: 'The user has linked you to the session "Pipeline" (abc) as its leader, and suggests...',
          attribution: { source_name: "mads", kind: "notice" },
        }])}
        links={[]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText("▸ [System] Leader tools provided to this session · expand to show message")).toBeTruthy();
  });

  it("eases the height a new pill replaces, then lets go", () => {
    vi.useFakeTimers();
    let height = 400;
    const spy = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(() => height);
    FLAGS.linkedSessions = true;
    const digest: TranscriptMessage = { role: "user", text: "d", attribution: { source_name: "S", kind: "digest" } };
    const { container, rerender } = render(<ChatRenderer rows={keyed([digest])} links={[]} viewedSessionId="v" />);
    height = 300;
    rerender(<ChatRenderer rows={keyed([digest, { role: "agent", text: "NOOP" }])} links={[]} viewedSessionId="v" />);
    const pill = container.querySelector("[data-ease]") as HTMLElement;
    expect(pill.style.getPropertyValue("--ease-from")).toBe("100px");
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(container.querySelector("[data-ease]")).toBeNull();
    spy.mockRestore();
    vi.useRealTimers();
  });
});
