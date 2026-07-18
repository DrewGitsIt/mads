import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LinkChooserDialog } from "./LinkChooserDialog";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("LinkChooserDialog", () => {
  const sessions = [
    makeSession({ id: "a", name: "investigator" }),
    makeSession({ id: "b", name: "payments refactor" }),
    makeSession({ id: "c", name: "gateway hardening" }),
  ];

  it("defaults to Leader preset and the first selected session as holder", () => {
    render(
      <LinkChooserDialog
        sessionIds={["a", "b", "c"]}
        sessions={sessions}
        routerClient={new FakeRouterClient()}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );
    expect(screen.getByText("Leader").closest("button")?.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("Who leads?")).toBeTruthy();
    expect(screen.getByText("Create 2 links")).toBeTruthy();
  });

  it("switches the role label and grant chips when Monitor is chosen", () => {
    render(
      <LinkChooserDialog
        sessionIds={["a", "b"]}
        sessions={sessions}
        routerClient={new FakeRouterClient()}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Monitor"));
    expect(screen.getByText("Who monitors?")).toBeTruthy();
  });

  it("creates a link per non-holder session on submit", async () => {
    const client = new FakeRouterClient();
    const createLinkSpy = vi.spyOn(client, "createLink");
    const onCreated = vi.fn();
    render(
      <LinkChooserDialog
        sessionIds={["a", "b", "c"]}
        sessions={sessions}
        routerClient={client}
        onClose={vi.fn()}
        onCreated={onCreated}
      />,
    );
    fireEvent.click(screen.getByText("Create 2 links"));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(createLinkSpy).toHaveBeenCalledTimes(2);
    expect(createLinkSpy).toHaveBeenCalledWith({
      granting_session_id: "a",
      subject_session_id: "b",
      preset: "leader",
    });
    expect(createLinkSpy).toHaveBeenCalledWith({
      granting_session_id: "a",
      subject_session_id: "c",
      preset: "leader",
    });
  });

  it("changes the holder when a different radio is picked", async () => {
    const client = new FakeRouterClient();
    const createLinkSpy = vi.spyOn(client, "createLink");
    render(
      <LinkChooserDialog
        sessionIds={["a", "b"]}
        sessions={sessions}
        routerClient={client}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );
    fireEvent.click(screen.getAllByRole("radio")[1]);
    fireEvent.click(screen.getByText("Create 1 link"));

    await waitFor(() =>
      expect(createLinkSpy).toHaveBeenCalledWith({
        granting_session_id: "b",
        subject_session_id: "a",
        preset: "leader",
      }),
    );
  });

  it("shows a RouterValidationError's field errors without closing", async () => {
    const client = new FakeRouterClient();
    // Seed the reverse direction so a->b is rejected as a duplicate-in-the-opposite-direction.
    await client.createLink({ granting_session_id: "b", subject_session_id: "a", preset: "monitor" });
    const onCreated = vi.fn();
    render(
      <LinkChooserDialog
        sessionIds={["a", "b"]}
        sessions={sessions}
        routerClient={client}
        onClose={vi.fn()}
        onCreated={onCreated}
      />,
    );
    fireEvent.click(screen.getByText("Create 1 link"));

    await waitFor(() => expect(screen.getByText(/opposite direction/)).toBeTruthy());
    expect(onCreated).not.toHaveBeenCalled();
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    render(
      <LinkChooserDialog
        sessionIds={["a", "b"]}
        sessions={sessions}
        routerClient={new FakeRouterClient()}
        onClose={onClose}
        onCreated={vi.fn()}
      />,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });
});
