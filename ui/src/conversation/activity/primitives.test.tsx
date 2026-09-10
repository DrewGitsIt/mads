import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Diffstat, KindIcon, McpBadge, StatusGlyph } from "./primitives";

describe("KindIcon", () => {
  it("renders the per-kind glyph on a tile", () => {
    render(<KindIcon kind="execute" />);
    const tile = document.querySelector('[data-kind="execute"]')!;
    expect(tile.textContent).toBe("❯");
  });

  it("renders each vocabulary kind with a distinct glyph", () => {
    const kinds = [
      "read",
      "search",
      "execute",
      "edit",
      "delete",
      "move",
      "fetch",
      "think",
      "agent",
      "mcp",
      "skill",
      "plan",
      "other",
    ] as const;
    render(
      <div>
        {kinds.map((k) => (
          <KindIcon key={k} kind={k} />
        ))}
      </div>,
    );
    const glyphs = kinds.map(
      (k) => document.querySelector(`[data-kind="${k}"]`)!.textContent,
    );
    expect(new Set(glyphs).size).toBe(kinds.length);
  });
});

describe("StatusGlyph", () => {
  it.each(["completed", "failed", "pending", "interrupted"] as const)(
    "renders %s",
    (status) => {
      render(<StatusGlyph status={status} />);
      expect(document.querySelector(`[data-status="${status}"]`)).toBeTruthy();
    },
  );

  it("renders in_progress as the running spinner", () => {
    render(<StatusGlyph status="in_progress" />);
    expect(screen.getByRole("img", { name: "in progress" })).toBeTruthy();
  });
});

describe("Diffstat", () => {
  it("renders adds and deletes", () => {
    render(<Diffstat adds={24} dels={6} />);
    expect(screen.getByText("+24")).toBeTruthy();
    expect(screen.getByText("−6")).toBeTruthy();
  });

  it("renders nothing for an empty stat", () => {
    const { container } = render(<Diffstat adds={0} dels={0} />);
    expect(container.firstChild).toBeNull();
  });
});

describe("McpBadge", () => {
  it("renders the server name", () => {
    render(<McpBadge server="playwright" />);
    expect(screen.getByText("playwright")).toBeTruthy();
  });
});
