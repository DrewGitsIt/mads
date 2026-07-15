import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { RetroHistoryList } from "./RetroHistoryList";
import { fixtureRetro } from "../../../router/fixtures";

describe("RetroHistoryList", () => {
  it("renders an empty state when there are no retros", () => {
    render(<RetroHistoryList retros={[]} onSelect={() => {}} />);
    expect(screen.getByText("No retros yet.")).toBeTruthy();
  });

  it("renders a row per retro and calls onSelect with its id when clicked", () => {
    const onSelect = vi.fn();
    render(<RetroHistoryList retros={[fixtureRetro]} onSelect={onSelect} />);
    fireEvent.click(screen.getByText("3 DPAs"));
    expect(onSelect).toHaveBeenCalledWith(fixtureRetro.id);
  });
});
