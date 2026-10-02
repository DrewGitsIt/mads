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
  // The trigger used to spell the cadence out ("WATCHED · ON SIGNAL") and the
  // row carried a second pill for the orchestrate state beside it, which on an
  // already-indented nested card stacked two shouted lines of configuration
  // under a one-line status. A cadence is a setting, and the flyout shows it as
  // the checked option — which is where a setting belongs.
  it("is one glyph pointing back up the connector, whatever the cadence", () => {
    for (const cadence of ["per_turn", "on_signal", "manual_only"] as const) {
      const { unmount } = render(
        <LinkOptionsMenu
          badge="WATCHED"
          subjectName="worker"
          linkId="l1"
          grantingSessionId="g1"
          subjectSessionId="s1"
          grants={["read"]}
          cadence={cadence}
          routerClient={new FakeRouterClient()}
          onUnlinkRequest={vi.fn()}
        />,
      );
      // Not the role spelled out, and not the cadence: the connector states
      // the relationship, and a setting is not news.
      expect(screen.getByRole("button", { name: "Link options for worker" }).textContent).toBe("⇱");
      unmount();
    }
  });

  it("names the cadence inside, as the checked option", () => {
    renderMenu({ cadence: "manual_only" });
    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    expect(
      screen.getByRole("menuitemradio", { name: /Catch up on request/ }).getAttribute("aria-checked"),
    ).toBe("true");
  });

  // Trouble is the exception: a degraded orchestrate grant will not fix itself,
  // so the trigger carries a mark for it — and the sentence on hover.
  it("marks the trigger, and says why, when the orchestrate grant is degraded", () => {
    renderMenu({ orchestrateStatus: "failed", orchestrateError: "mads not found" });
    const trigger = screen.getByRole("button", { name: "Link options for worker" });
    expect(trigger.querySelector("span[aria-hidden]")).toBeTruthy();
    expect(trigger.getAttribute("title")).toBe(
      "Orchestration tools failed to start. mads not found",
    );
  });

  it("leaves the trigger unmarked for a grant that is merely waiting to mount", () => {
    // pending_restart is not news: the tools mount at the leader's next idle
    // moment with nobody doing anything.
    renderMenu({ orchestrateStatus: "pending_restart" });
    const trigger = screen.getByRole("button", { name: "Link options for worker" });
    // The glyph itself is aria-hidden, so the mark is the only other child.
    expect(trigger.childElementCount).toBe(1);
    expect(trigger.getAttribute("title")).toBeNull();
  });

  it("changes the update policy, resending the grants unchanged", async () => {
    const client = new FakeRouterClient();
    await client.createLink({ granting_session_id: "g1", subject_session_id: "s1", preset: "monitor" });
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    renderMenu({ linkId: "link-1", routerClient: client });

    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    expect(screen.getByRole("menuitemradio", { name: /Follow intently/ }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("Wake on signals")).toBeTruthy();
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
    expect(screen.getByText(/activate when the leading agent is next idle/)).toBeTruthy();
  });

  it("says why a leader link's orchestration tools failed", () => {
    renderMenu({ badge: "LINKED", orchestrateStatus: "failed", orchestrateError: "start the orchestrate tools: not found" });
    fireEvent.click(screen.getByRole("button", { name: "Link options for worker" }));
    expect(screen.getByText("Orchestration tools failed to start. start the orchestrate tools: not found")).toBeTruthy();
  });
});
