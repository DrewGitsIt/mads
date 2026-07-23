import { describe, expect, it } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { RailProposalList } from "./RailProposalList";
import { FakeRouterClient, fixtureProposals, fixtureTasks } from "../router/fixtures";
import type { Task } from "../router/types";

const tasksById: Record<string, Task> = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

describe("RailProposalList", () => {
  it("renders a card per pending proposal", () => {
    render(
      <RailProposalList
        proposals={fixtureProposals}
        tasksById={tasksById}
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.getByText("New task")).toBeTruthy();
    expect(screen.getByText("Note")).toBeTruthy();
    expect(screen.getByText("Update")).toBeTruthy();
  });

  it("never renders a dismissed proposal", () => {
    render(
      <RailProposalList
        proposals={[{ ...fixtureProposals[0], status: "dismissed" }]}
        tasksById={tasksById}
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.queryByText("New task")).toBeNull();
  });

  it("renders null when there are no visible proposals", () => {
    const { container } = render(
      <RailProposalList proposals={[]} tasksById={tasksById} routerClient={new FakeRouterClient()} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders an already-approved proposal (auto-approve) as a static notice", () => {
    render(
      <RailProposalList
        proposals={[{ ...fixtureProposals[0], status: "approved" }]}
        tasksById={tasksById}
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.getByText("Applied automatically")).toBeTruthy();
  });

  it("hides a proposal immediately after manual approve, without waiting for a refetch", async () => {
    const client = new FakeRouterClient();
    render(
      <RailProposalList
        proposals={[fixtureProposals[0]]}
        tasksById={tasksById}
        routerClient={client}
      />,
    );
    expect(screen.getByText("New task")).toBeTruthy();
    fireEvent.click(screen.getByText("Approve"));
    await waitFor(() => expect(screen.queryByText("New task")).toBeNull());
    expect(screen.queryByText("Applied automatically")).toBeNull();
  });

  it("hides a proposal immediately after manual dismiss", async () => {
    const client = new FakeRouterClient();
    render(
      <RailProposalList
        proposals={[fixtureProposals[0]]}
        tasksById={tasksById}
        routerClient={client}
      />,
    );
    fireEvent.click(screen.getByText("Dismiss"));
    await waitFor(() => expect(screen.queryByText("New task")).toBeNull());
  });
});
