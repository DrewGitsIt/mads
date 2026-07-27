import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import { FLAGS } from "../../featureFlags";
import type { Link, TranscriptMessage } from "../../router/types";

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

  it("renders tool messages as collapsed ToolCards", () => {
    const messages: TranscriptMessage[] = [
      { role: "tool", tool: { name: "Read", path: "a.go" } },
    ];
    render(<ChatRenderer messages={messages} />);
    const button = screen.getByRole("button");
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByText("Read")).toBeTruthy();
  });

  it("folds a tool_result into its tool_use's card — one card per call, zero standalone result cards", () => {
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

    expect(screen.getAllByRole("button")).toHaveLength(2);
    expect(screen.queryByText("no input")).toBeNull();

    const [readButton, bashButton] = screen.getAllByRole("button");
    expect(readButton.textContent).toContain("error");
    expect(bashButton.textContent).toContain("ok");
    expect(bashButton.textContent).toContain("5.0s");
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
});
