import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { RailChangesList } from "./RailChangesList";
import type { ChangeSet } from "../router/types";
import { file } from "../files/testChangeFile";

const CS: ChangeSet = {
  baseline: "start",
  baselines: [{ id: "start", label: "Session start", available: true }],
  git: true,
  files: [
    file("internal/auth/token.go", { added: 40, deleted: 2, turns: [2] }),
    file("cmd/login.go", { added: 120, turns: [4] }),
    file("internal/auth/token_test.go", { tier: "tests", added: 30, turns: [3] }),
    file("out.json", { tier: "scratch", change: "added", added: 300, turns: [3] }),
  ],
  turns: [
    { n: 1, prompt: "Plan the login", at: "", files: 0 },
    { n: 2, prompt: "Store the token in the keychain, not a file. ".repeat(4).trim(), at: "", files: 1 },
    { n: 3, prompt: "Test it against staging", at: "", files: 2 },
    { n: 4, prompt: "Wire it into the CLI", at: "", files: 1 },
  ],
  commits: [],
  totals: { files: 4, added: 490, deleted: 2 },
  computed_at: "",
};

const never = () => false;

function names() {
  return screen
    .getAllByRole("button")
    .filter((b) => b.title.startsWith("/w/"))
    .map((b) => b.title);
}

describe("RailChangesList", () => {
  it("defaults to Source, all turns, files by size with the filename first and the folder beneath", () => {
    render(<RailChangesList changeSet={CS} isUnseen={never} />);
    expect(screen.getByText("Source (2)")).toBeTruthy();
    expect(screen.getByTestId("rail-turn").textContent).toBe("All turns");
    expect(names()).toEqual(["/w/cmd/login.go", "/w/internal/auth/token.go"]);
    const row = screen.getByText("token.go").closest("button")!;
    expect(row.textContent).toContain("internal/auth");
  });

  it("the tier dropdown has All files and every tier with counts", () => {
    render(<RailChangesList changeSet={CS} isUnseen={never} />);
    fireEvent.click(screen.getByText("Source (2)"));
    for (const label of ["All files (4)", "Tests & fixtures (1)", "Config & dependencies (0)", "Generated (0)", "Scratch (1)"]) {
      expect(screen.getByRole("option", { name: label })).toBeTruthy();
    }
    fireEvent.click(screen.getByRole("option", { name: "All files (4)" }));
    expect(names()).toHaveLength(4);
  });

  it("the arrows skip turns with nothing in the tier, end on Latest, and show the prompt", () => {
    render(<RailChangesList changeSet={CS} isUnseen={never} />);
    const next = screen.getByRole("button", { name: "Next turn" });
    const prev = screen.getByRole("button", { name: "Previous turn" });
    expect((prev as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(next);
    expect(screen.getByTestId("rail-turn").textContent).toBe("Turn 2 of 4");
    expect(names()).toEqual(["/w/internal/auth/token.go"]);
    expect(screen.getByTitle(CS.turns[1].prompt).textContent).toMatch(/^“Store the token.*…”$/);

    fireEvent.click(next); // turn 3 has no source files: skipped
    expect(screen.getByTestId("rail-turn").textContent).toBe("Turn 4 of 4");
    fireEvent.click(next);
    expect(screen.getByTestId("rail-turn").textContent).toBe("● Latest following turn 4");
    expect((next as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(prev);
    expect(screen.getByTestId("rail-turn").textContent).toBe("Turn 4 of 4");
  });

  it("says when the tier has nothing in the turn", () => {
    render(<RailChangesList changeSet={CS} isUnseen={never} />);
    fireEvent.click(screen.getByText("Source (2)"));
    fireEvent.click(screen.getByRole("option", { name: "Scratch (1)" }));
    fireEvent.click(screen.getByRole("button", { name: "Next turn" }));
    expect(screen.getByTestId("rail-turn").textContent).toBe("Turn 3 of 4");
    expect(screen.getByText("“Test it against staging”")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Next turn" }));
    expect(screen.getByText("No scratch changes in this turn.")).toBeTruthy();
  });

  it("footer counts shown and new files", () => {
    render(<RailChangesList changeSet={CS} isUnseen={(f) => f.display === "cmd/login.go"} />);
    expect(screen.getByText(/2 shown/).textContent).toMatch(/2 shown · +1 new/);
  });

  it("a file click opens the Changes tab on it with the rail's tier and turn; the footer link carries tier and turn", () => {
    const onOpen = vi.fn();
    render(<RailChangesList changeSet={CS} isUnseen={never} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button", { name: "Next turn" }));
    fireEvent.click(screen.getByText("token.go"));
    expect(onOpen).toHaveBeenLastCalledWith({ path: "/w/internal/auth/token.go", tier: "source", turn: 2 });
    fireEvent.click(screen.getByRole("button", { name: "Open in Changes →" }));
    expect(onOpen).toHaveBeenLastCalledWith({ tier: "source", turn: 2 });
  });

  it("rows are inert and the link hidden without onOpen", () => {
    render(<RailChangesList changeSet={CS} isUnseen={never} />);
    expect((screen.getByText("token.go").closest("button") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByText("Open in Changes →")).toBeNull();
  });

  it("shows loading and error states", () => {
    const { rerender } = render(<RailChangesList changeSet={null} loading isUnseen={never} />);
    expect(screen.getByText("Loading changes…")).toBeTruthy();
    rerender(<RailChangesList changeSet={null} error="boom" isUnseen={never} />);
    expect(screen.getByText("Couldn't load changes: boom")).toBeTruthy();
  });
});
