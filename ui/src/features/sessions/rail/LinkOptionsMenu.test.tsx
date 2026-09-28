import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LinkOptionsMenu } from "./LinkOptionsMenu";
import type { LinkOptionsMenuProps } from "./LinkOptionsMenu";
import { FakeRouterClient } from "../../../router/fixtures";

function renderMenu(overrides: Partial<LinkOptionsMenuProps> = {}) {
  const props: LinkOptionsMenuProps = {
    badge: "WATCHED",
    subjectName: "worker",
    linkId: "l1",
    grantingSessionId: "g1",
    subjectSessionId: "s1",
    grants: ["read"],
    cadence: "per_turn",
    routerClient: new FakeRouterClient(),
    onUnlinkRequest: vi.fn(),
    ...overrides,
  };
  render(<LinkOptionsMenu {...props} />);
  return props;
}

describe("LinkOptionsMenu", () => {
  it("uses the role badge as its trigger, flagging manual-only links", () => {
    renderMenu({ cadence: "manual_only" });
    const trigger = screen.getByRole("button", { name: "Link options for worker" });
    expect(trigger.textContent).toBe("WATCHED· MANUAL");
  });

  it("shows just the badge for the default debounced cadence", () => {
    renderMenu();
    expect(screen.getByRole("button", { name: "Link options for worker" }).textContent).toBe("WATCHED");
  });

  it("changes the update policy, resending the grants unchanged", async () => {
    const client = new FakeRouterClient();
    await client.createLink({ granting_session_id: "g1", subject_session_id: "s1", preset: "monitor" });
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    renderMenu({ linkId: "link-1", routerClient: client });

    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    expect(screen.getByRole("menuitemradio", { name: /Follow intently/ }).getAttribute("aria-checked")).toBe("true");
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

  it("does not call updateLink when the current cadence is re-selected", () => {
    const client = new FakeRouterClient();
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    renderMenu({ routerClient: client });
    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    fireEvent.click(screen.getByText("Follow intently"));
    expect(updateLinkSpy).not.toHaveBeenCalled();
  });

  it("offers Unlink, closing the flyout before asking for confirmation", () => {
    const props = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Unlink…" }));
    expect(props.onUnlinkRequest).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("explains a leader link whose orchestration tools are pending", () => {
    renderMenu({ badge: "LINKED", orchestrateStatus: "pending_restart" });
    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    expect(screen.getByText(/next time the leader restarts/)).toBeTruthy();
  });
});
