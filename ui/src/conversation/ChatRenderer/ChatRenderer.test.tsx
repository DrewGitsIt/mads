import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatRenderer } from "./ChatRenderer";
import type { TranscriptMessage } from "../../router/types";

describe("ChatRenderer", () => {
  it("renders user and agent prose through the markdown renderer", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "hello **there**" },
      { role: "agent", text: "hi back" },
    ];
    render(<ChatRenderer messages={messages} />);
    expect(screen.getByText("there").tagName).toBe("STRONG");
    expect(screen.getByText("hi back")).toBeTruthy();
  });

  it("renders injected messages with a distinct attribution chip, never as the user's own bubble", () => {
    const messages: TranscriptMessage[] = [
      {
        role: "user",
        text: "do the thing",
        attribution: { link_id: "lnk_1", source_name: "Leader Session" },
      },
    ];
    const { container } = render(<ChatRenderer messages={messages} />);
    expect(screen.getByText(/Leader Session/)).toBeTruthy();
    const wrapper = container.querySelector('[class*="stream"]')
      ?.firstElementChild;
    expect(wrapper?.className).toMatch(/injected/);
    expect(wrapper?.className).not.toMatch(/\buser\b/);
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
});
