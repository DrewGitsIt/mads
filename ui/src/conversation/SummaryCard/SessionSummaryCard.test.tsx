// @vitest-environment jsdom
// Markdown renders through DOMPurify, which doesn't sanitize correctly
// under happy-dom (see ui/src/conversation/ChatRenderer/markdown.test.tsx).
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SessionSummaryCard } from "./SessionSummaryCard";
import { fixtureSessions } from "../../router/fixtures";

describe("SessionSummaryCard", () => {
  it("renders the summary markdown for an inactive session", () => {
    render(<SessionSummaryCard session={fixtureSessions["session-inactive"]} />);
    expect(screen.getByText("Accomplished")).toBeTruthy();
    expect(screen.getByText(/Reproduced the flake locally/)).toBeTruthy();
  });

  it("renders nothing for an active session", () => {
    const { container } = render(
      <SessionSummaryCard session={fixtureSessions["session-active"]} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders nothing for an inactive session with no summary", () => {
    const { container } = render(
      <SessionSummaryCard session={{ ...fixtureSessions["session-inactive"], summary: undefined }} />,
    );
    expect(container.innerHTML).toBe("");
  });
});
