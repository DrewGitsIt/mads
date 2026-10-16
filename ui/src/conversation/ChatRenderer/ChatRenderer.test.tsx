import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { FLAGS } from "../../featureFlags";
import type { Link, PermissionRequest, TranscriptMessage } from "../../router/types";

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
    render(<ChatRenderer messages={messages} />);
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
    const { container } = render(<ChatRenderer messages={messages} />);
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
    render(<ChatRenderer messages={[{ role: "user", text }]} attachmentUrl={url} />);
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
      <ChatRenderer messages={[{ role: "user", text }]} attachmentUrl={(id) => `http://r/${id}`} />,
    );
    fireEvent.error(screen.getByAltText("huge.png"));
    expect(screen.getByText("huge.png")).toBeTruthy();
    expect(screen.queryByAltText("huge.png")).toBeNull();
  });

  it("renders tool messages as a collapsed activity group that expands to rows", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool", tool: { name: "Read", path: "a.go" } },
    ];
    render(<ChatRenderer messages={messages} />);
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
    render(<ChatRenderer messages={messages} />);

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
        attribution: { link_id: "lnk_1", source_name: "Leader Session" },
      },
    ];
    const { container } = render(
      <ChatRenderer messages={messages} links={[makeLink({})]} viewedSessionId="subject-id" />,
    );
    expect(screen.queryByText(/Leader Session/)).toBeNull();
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/_user_/);
    expect(wrapper?.className).not.toMatch(/injected/);
  });
});

describe("ChatRenderer — a notice from mads itself", () => {
  it("renders an attributed message with no link as a notice, never the user's bubble, flag or not", () => {
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "A pipeline task merged into your folder while you were working",
        attribution: { link_id: "", source_name: "your pipeline run" },
      },
    ];
    const { container } = render(<ChatRenderer messages={messages} />);
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

  it("renders an inject chip, never the user's own bubble, when the viewer is the link's subject", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "do the thing",
        attribution: { link_id: "lnk_1", source_name: "Leader Session" },
      },
    ];
    const { container } = render(
      <ChatRenderer messages={messages} links={[makeLink({})]} viewedSessionId="subject-id" />,
    );
    expect(screen.getByText(/Leader Session/)).toBeTruthy();
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/injected/);
    expect(wrapper?.className).not.toMatch(/\buser\b/);
  });

  it("renders a digest card with the cadence line when the viewer is the link's granter", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "compressed summary of the turn",
        attribution: { link_id: "lnk_1", source_name: "payments refactor" },
      },
    ];
    render(
      <ChatRenderer
        messages={messages}
        links={[makeLink({ cadence: "per_turn" })]}
        viewedSessionId="leader-id"
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
        attribution: { link_id: "lnk_1", source_name: "payments refactor" },
      },
    ];
    render(
      <ChatRenderer
        messages={messages}
        links={[makeLink({ cadence: "manual_only" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText("cadence: manual — no automatic digests")).toBeTruthy();
  });

  it("falls back to the inject chip when the attributed link isn't resolved yet", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "do the thing",
        attribution: { link_id: "lnk_missing", source_name: "Leader Session" },
      },
    ];
    const { container } = render(
      <ChatRenderer messages={messages} links={[]} viewedSessionId="subject-id" />,
    );
    const wrapper = container.querySelector('[class*="stream"]')?.firstElementChild;
    expect(wrapper?.className).toMatch(/injected/);
  });

  it("collapses a digest answered with exactly NOOP into a quiet-check chip", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "routine progress digest",
        attribution: { link_id: "lnk_1", source_name: "payments refactor" },
      },
      { role: "agent", text: "NOOP" },
    ];
    render(
      <ChatRenderer
        messages={messages}
        links={[makeLink({ cadence: "on_signal" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/1 quiet check · payments refactor/)).toBeTruthy();
    expect(screen.queryByText("routine progress digest")).toBeNull();
    expect(screen.queryByText("NOOP")).toBeNull();
  });

  it("aggregates consecutive quiet checks into one chip, and leaves a real reply alone", () => {
    FLAGS.linkedSessions = true;
    const digest = (text: string): TranscriptMessage => ({
      role: "user",
      text,
      attribution: { link_id: "lnk_1", source_name: "payments refactor" },
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
        messages={messages}
        links={[makeLink({ cadence: "on_signal" })]}
        viewedSessionId="leader-id"
      />,
    );
    expect(screen.getByText(/2 quiet checks · payments refactor/)).toBeTruthy();
    // The third digest got a real reply — both render in full.
    expect(screen.getByText("digest three")).toBeTruthy();
    expect(screen.getByText("I'll step in and unblock the task.")).toBeTruthy();
  });

  it("never collapses a NOOP-shaped reply to the subject's own inject chip", () => {
    FLAGS.linkedSessions = true;
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "kick off the refactor",
        attribution: { link_id: "lnk_1", source_name: "Leader Session" },
      },
      { role: "agent", text: "NOOP" },
    ];
    // Viewed from the subject side, the attributed message is an inject
    // chip, not a digest card — the reply stays visible.
    render(
      <ChatRenderer
        messages={messages}
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
        messages={transcript}
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
        messages={transcript}
        permissionRequests={[req({ tool: "Bash", tool_use_id: "sub1", parent_tool_use_id: "tu1" })]}
      />,
    );
    expect(cardsIn(screen.getByTestId("task-card"))).toBe(1);
    expect(cardsIn(screen.getByTestId("activity-group"))).toBe(0);
  });

  it("falls back to the trailing list without a tool_use_id, or when the call isn't rendered yet", () => {
    const { container } = render(
      <ChatRenderer
        messages={transcript}
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
    render(<ChatRenderer messages={failed} />);
    const note = screen.getByRole("status");
    expect(note.textContent).toContain("Not delivered");
    expect(note.textContent).toContain("Login expired · Please run /login");
    // The error is no longer an agent bubble of its own.
    expect(screen.getAllByText("Login expired · Please run /login")).toHaveLength(1);
  });

  it("offers Retry on the latest message between turns, resending its text", () => {
    const onRetry = vi.fn();
    const { rerender } = render(<ChatRenderer messages={failed} onRetry={onRetry} />);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledWith("deploy it");

    rerender(<ChatRenderer messages={failed} onRetry={onRetry} turnActive />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();

    rerender(<ChatRenderer messages={[...failed, { role: "user", text: "something else" }]} onRetry={onRetry} />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("collapses a successful retry to one message with an expandable failed-attempt count", () => {
    render(
      <ChatRenderer
        messages={[...failed, { role: "user", text: "deploy it" }, { role: "agent", text: "deployed" }]}
      />,
    );
    expect(screen.getAllByText("deploy it")).toHaveLength(1);
    expect(screen.queryByText("Not delivered")).toBeNull();
    const count = screen.getByRole("button", { name: "1 failed attempt" });
    expect(screen.queryByText("Login expired · Please run /login")).toBeNull();
    fireEvent.click(count);
    expect(screen.getByText("Login expired · Please run /login")).toBeTruthy();
  });
});
