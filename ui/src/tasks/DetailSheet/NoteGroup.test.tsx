import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { NoteGroup } from "./NoteGroup";
import type { Note } from "../../router/types";

const draftNote: Note = {
  id: 1,
  type: "risk",
  content: "Might mask a persistent upstream outage.",
  source: "synthesizer",
  status: "draft",
  timestamp: "2026-07-01T11:30:00Z",
};

const confirmedNote: Note = {
  id: 2,
  type: "decision",
  content: "Use exponential backoff.",
  source: "user",
  status: "confirmed",
  timestamp: "2026-07-01T11:00:00Z",
};

describe("NoteGroup", () => {
  it("renders nothing when there are no notes of this type", () => {
    const { container } = render(
      <NoteGroup type="risk" label="Risk" notes={[]} onConfirm={vi.fn()} onDiscard={vi.fn()} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("shows draft provenance styling and Confirm/Discard for draft notes", () => {
    const onConfirm = vi.fn();
    const onDiscard = vi.fn();
    render(
      <NoteGroup
        type="risk"
        label="Risk"
        notes={[draftNote]}
        onConfirm={onConfirm}
        onDiscard={onDiscard}
      />,
    );
    expect(screen.getByText("Draft")).toBeTruthy();
    fireEvent.click(screen.getByText("Confirm"));
    expect(onConfirm).toHaveBeenCalledWith(1);
    fireEvent.click(screen.getByText("Discard"));
    expect(onDiscard).toHaveBeenCalledWith(1);
  });

  it("renders confirmed notes without Confirm/Discard actions", () => {
    render(
      <NoteGroup
        type="decision"
        label="Decision"
        notes={[confirmedNote]}
        onConfirm={vi.fn()}
        onDiscard={vi.fn()}
      />,
    );
    expect(screen.getByText("Use exponential backoff.")).toBeTruthy();
    expect(screen.queryByText("Confirm")).toBeNull();
  });

  it("collapses when the heading is clicked", () => {
    render(
      <NoteGroup
        type="decision"
        label="Decision"
        notes={[confirmedNote]}
        onConfirm={vi.fn()}
        onDiscard={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText(/Decision/));
    expect(screen.queryByText("Use exponential backoff.")).toBeNull();
  });
});
