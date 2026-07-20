import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SuggestionChips } from "./SuggestionChips";
import type { Session, Task } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "Worktree merge-back UX",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: "t1",
    session_id: "s1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    source: "user",
    title: "Fix merge conflict UI",
    ...overrides,
  };
}

describe("SuggestionChips", () => {
  it("renders a chip from an attention session and a chip from a ready task, and reports clicks with structured navigation, not label text", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.includes("/sessions")) {
        return Promise.resolve(new Response(JSON.stringify([makeSession({})]), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify([makeTask({})]), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const onPick = vi.fn();

    render(<SuggestionChips onPick={onPick} />);

    await waitFor(() => expect(screen.getByText("From your open work")).toBeTruthy());
    const resumeChip = screen.getByText(/Resume: Worktree merge-back UX/);
    fireEvent.click(resumeChip);
    expect(onPick).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "resume", sessionId: "s1" }),
    );

    const pickupChip = screen.getByText(/Pick up: Fix merge conflict UI/);
    fireEvent.click(pickupChip);
    expect(onPick).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "pickup", sessionId: "s1", taskId: "t1" }),
    );

    vi.unstubAllGlobals();
  });

  it("renders nothing when there is no open work", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response("[]", { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const { container } = render(<SuggestionChips onPick={() => {}} />);
    await waitFor(() => expect(container.textContent).toBe(""));
    vi.unstubAllGlobals();
  });
});
