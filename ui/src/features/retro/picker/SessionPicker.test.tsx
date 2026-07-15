import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SessionPicker } from "./SessionPicker";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "Session One",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("SessionPicker", () => {
  const sessions = [
    makeSession({ id: "s1", name: "Work session" }),
    makeSession({ id: "s2", name: "Retro follow-up", tags: ["retro"] }),
  ];

  it("renders a checkbox per session and reports toggles", () => {
    const onToggle = vi.fn();
    render(
      <SessionPicker sessions={sessions} selectedIds={[]} onToggle={onToggle} onClear={() => {}} />,
    );

    fireEvent.click(screen.getByLabelText("Work session"));
    expect(onToggle).toHaveBeenCalledWith("s1");
  });

  it("shows checked state for selected sessions", () => {
    render(
      <SessionPicker sessions={sessions} selectedIds={["s2"]} onToggle={() => {}} onClear={() => {}} />,
    );
    expect((screen.getByLabelText("Retro follow-up") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Work session") as HTMLInputElement).checked).toBe(false);
  });

  it("filters to retro-generated sessions only", () => {
    render(
      <SessionPicker sessions={sessions} selectedIds={[]} onToggle={() => {}} onClear={() => {}} />,
    );
    fireEvent.click(screen.getByText("Retro-generated"));
    expect(screen.queryByLabelText("Work session")).toBeNull();
    expect(screen.getByLabelText("Retro follow-up")).toBeTruthy();
  });

  it("disables clear-selection when nothing is selected, enables it otherwise", () => {
    const { rerender } = render(
      <SessionPicker sessions={sessions} selectedIds={[]} onToggle={() => {}} onClear={() => {}} />,
    );
    expect(screen.getByText("Clear selection").closest("button")?.disabled).toBe(true);

    rerender(
      <SessionPicker sessions={sessions} selectedIds={["s1"]} onToggle={() => {}} onClear={() => {}} />,
    );
    expect(screen.getByText("Clear selection").closest("button")?.disabled).toBe(false);
  });

  it("calls onClear when clicked", () => {
    const onClear = vi.fn();
    render(
      <SessionPicker sessions={sessions} selectedIds={["s1"]} onToggle={() => {}} onClear={onClear} />,
    );
    fireEvent.click(screen.getByText("Clear selection"));
    expect(onClear).toHaveBeenCalledOnce();
  });
});
