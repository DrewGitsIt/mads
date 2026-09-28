import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LinkCadenceControl } from "./LinkCadenceControl";
import { FakeRouterClient } from "../../../router/fixtures";

describe("LinkCadenceControl", () => {
  it("shows honest debounced-digest copy for per_turn, not a literal per-turn claim", () => {
    render(
      <LinkCadenceControl
        linkId="l1"
        grantingSessionId="g1"
        subjectSessionId="s1"
        grants={["read"]}
        cadence="per_turn"
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.getByText("DEBOUNCED DIGESTS")).toBeTruthy();
  });

  it("shows manual-only badge copy for manual_only", () => {
    render(
      <LinkCadenceControl
        linkId="l1"
        grantingSessionId="g1"
        subjectSessionId="s1"
        grants={["read"]}
        cadence="manual_only"
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.getByText("MANUAL ONLY")).toBeTruthy();
  });

  it("opens a menu on click and calls updateLink with the resent grants and new cadence", async () => {
    const client = new FakeRouterClient();
    await client.createLink({ granting_session_id: "g1", subject_session_id: "s1", preset: "monitor" });
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    render(
      <LinkCadenceControl
        linkId="link-1"
        grantingSessionId="g1"
        subjectSessionId="s1"
        grants={["read"]}
        cadence="per_turn"
        routerClient={client}
      />,
    );

    fireEvent.click(screen.getByText("DEBOUNCED DIGESTS"));
    expect(screen.getByText("Catch up on request")).toBeTruthy();
    fireEvent.click(screen.getByText("Catch up on request"));

    await waitFor(() =>
      expect(updateLinkSpy).toHaveBeenCalledWith("link-1", {
        granting_session_id: "g1",
        subject_session_id: "s1",
        grants: ["read"],
        cadence: "manual_only",
      }),
    );
  });

  it("closes the menu without calling updateLink when the current cadence is reselected", () => {
    const client = new FakeRouterClient();
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    render(
      <LinkCadenceControl
        linkId="link-1"
        grantingSessionId="g1"
        subjectSessionId="s1"
        grants={["read"]}
        cadence="per_turn"
        routerClient={client}
      />,
    );

    fireEvent.click(screen.getByText("DEBOUNCED DIGESTS"));
    fireEvent.click(screen.getByText("Follow intently"));
    expect(updateLinkSpy).not.toHaveBeenCalled();
  });
});
