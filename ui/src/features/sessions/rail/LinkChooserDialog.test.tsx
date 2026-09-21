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
      cadence: "on_signal",
    });
    expect(createLinkSpy).toHaveBeenCalledWith({
      granting_session_id: "a",
      subject_session_id: "c",
      preset: "leader",
      cadence: "on_signal",
    });
  });

  it("defaults to the Wake on signals (on_signal) cadence", () => {
    render(
      <LinkChooserDialog
        sessionIds={["a", "b"]}
        sessions={sessions}
        routerClient={new FakeRouterClient()}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Wake on signals").closest("button")?.getAttribute("aria-selected"),
    ).toBe("true");
  });

  it("sends manual_only cadence when Catch up on request is chosen", async () => {
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
    fireEvent.click(screen.getByText("Catch up on request"));
    fireEvent.click(screen.getByText("Create 1 link"));

    await waitFor(() =>
      expect(createLinkSpy).toHaveBeenCalledWith({
        granting_session_id: "a",
        subject_session_id: "b",
        preset: "leader",
        cadence: "manual_only",
      }),
    );
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
        cadence: "on_signal",
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

  describe("staged mode (Launchpad link-at-create)", () => {
    function renderStaged(onStage = vi.fn(), client = new FakeRouterClient()) {
      render(
        <LinkChooserDialog
          sessionIds={[]}
          sessions={sessions}
          routerClient={client}
          onClose={vi.fn()}
          stagedHolder={{ label: "This session (not started yet)", onStage }}
        />,
      );
      return onStage;
    }

    it("preselects the phantom holder row and disables the radios of checked sessions", () => {
      renderStaged();
      const phantom = screen.getByRole("radio", { name: /This session \(not started yet\)/ });
      expect((phantom as HTMLInputElement).checked).toBe(true);
      expect((phantom as HTMLInputElement).disabled).toBe(false);

      fireEvent.click(screen.getByRole("checkbox", { name: "investigator" }));
      const radios = screen.getAllByRole("radio") as HTMLInputElement[];
      expect(radios).toHaveLength(2);
      expect(radios[1].disabled).toBe(true);
      expect(radios[0].checked).toBe(true);
    });

    it("disables the stage button until a subject is checked", () => {
      renderStaged();
      const button = screen.getByText("Stage 0 links").closest("button");
      expect(button?.disabled).toBe(true);

      fireEvent.click(screen.getByRole("checkbox", { name: "investigator" }));
      expect(screen.getByText("Stage 1 link").closest("button")?.disabled).toBe(false);
    });

    it("confirm calls onStage with placeholder requests and never createLink", () => {
      const client = new FakeRouterClient();
      const createLinkSpy = vi.spyOn(client, "createLink");
      const onStage = renderStaged(vi.fn(), client);

      fireEvent.click(screen.getByRole("checkbox", { name: "investigator" }));
      fireEvent.click(screen.getByRole("checkbox", { name: "payments refactor" }));
      fireEvent.click(screen.getByText("Monitor"));
      fireEvent.click(screen.getByText("Stage 2 links"));

      expect(onStage).toHaveBeenCalledWith([
        { granting_session_id: "$new", subject_session_id: "a", preset: "monitor", cadence: "on_signal" },
        { granting_session_id: "$new", subject_session_id: "b", preset: "monitor", cadence: "on_signal" },
      ]);
      expect(createLinkSpy).not.toHaveBeenCalled();
    });

    it("seeds subjects and preset from initialSubjectIds/initialPreset when reopening", () => {
      render(
        <LinkChooserDialog
          sessionIds={[]}
          sessions={sessions}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
          stagedHolder={{ label: "This session (not started yet)", onStage: vi.fn() }}
          initialSubjectIds={["b"]}
          initialPreset="monitor"
          initialCadence="manual_only"
        />,
      );
      const checkbox = screen.getByRole("checkbox", { name: "payments refactor" });
      expect((checkbox as HTMLInputElement).checked).toBe(true);
      expect(screen.getByText("Monitor").closest("button")?.getAttribute("aria-selected")).toBe(
        "true",
      );
      expect(
        screen.getByText("Catch up on request").closest("button")?.getAttribute("aria-selected"),
      ).toBe("true");
      expect(screen.getByText("Stage 1 link")).toBeTruthy();
    });
  });
});
