import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FollowUpCta } from "./FollowUpCta";

describe("FollowUpCta", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the accepted count and creates a follow-up session on click", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          session: {
            id: "s-new",
            name: "Follow-up",
            activity: "active",
            created_at: "2026-07-14T00:00:00Z",
            updated_at: "2026-07-14T00:00:00Z",
          },
          addr: "localhost:0",
          tasks: [{ id: "t1" }],
        }),
        { status: 201 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const onCreated = vi.fn();

    render(<FollowUpCta retroId="retro-1" acceptedCount={2} onCreated={onCreated} />);
    expect(screen.getByText("Create follow-up session (2 accepted)")).toBeTruthy();

    fireEvent.click(screen.getByText("Create follow-up session (2 accepted)"));

    await waitFor(() => expect(onCreated).toHaveBeenCalledOnce());
    expect(onCreated).toHaveBeenCalledWith({
      session: expect.objectContaining({ id: "s-new" }),
      tasks: [{ id: "t1" }],
    });
    expect(fetchMock.mock.calls[0][0].toString()).toContain("/retros/retro-1/follow-up-session");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST" });
  });

  it("disables the button when there are 0 accepted DPAs", () => {
    render(<FollowUpCta retroId="retro-1" acceptedCount={0} />);
    expect(
      screen.getByText("Create follow-up session (0 accepted)").closest("button")?.disabled,
    ).toBe(true);
  });

  it("shows a created indicator instead of the CTA once follow_up_session_id is set", () => {
    render(<FollowUpCta retroId="retro-1" acceptedCount={2} followUpSessionId="s-existing" />);
    expect(screen.getByText("Follow-up session created")).toBeTruthy();
    expect(screen.queryByText(/Create follow-up session/)).toBeNull();
  });
});
