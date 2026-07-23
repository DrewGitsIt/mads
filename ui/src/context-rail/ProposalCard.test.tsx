import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ProposalCard } from "./ProposalCard";
import { FakeRouterClient, fixtureProposals } from "../router/fixtures";
import type { Proposal } from "../router/types";

function proposal(id: string): Proposal {
  return fixtureProposals.find((p) => p.id === id)!;
}

describe("ProposalCard", () => {
  it("renders a create_task proposal's kind and content", () => {
    render(<ProposalCard proposal={proposal("prop_new_task")} routerClient={new FakeRouterClient()} />);
    expect(screen.getByText("New task")).toBeTruthy();
    expect(screen.getByText("Add a metrics counter for retry exhaustion")).toBeTruthy();
  });

  it("renders an add_note proposal's target task and note content", () => {
    render(
      <ProposalCard
        proposal={proposal("prop_note")}
        routerClient={new FakeRouterClient()}
        targetTaskTitle="Write regression test for the retry race"
      />,
    );
    expect(screen.getByText("Note")).toBeTruthy();
    expect(screen.getByText("Write regression test for the retry race")).toBeTruthy();
    expect(
      screen.getByText("The retry race repro relies on a pinned test clock — flaky under -race."),
    ).toBeTruthy();
  });

  it("renders an update_task proposal's changed-field summary", () => {
    render(
      <ProposalCard
        proposal={proposal("prop_update")}
        routerClient={new FakeRouterClient()}
        targetTaskTitle="Add retry backoff to the ingest worker"
      />,
    );
    expect(screen.getByText("Update")).toBeTruthy();
    expect(screen.getByText("Proposed changes to acceptance criteria.")).toBeTruthy();
  });

  it("approves via the Router client and reports the resolved proposal", async () => {
    const client = new FakeRouterClient();
    const onApproved = vi.fn();
    render(
      <ProposalCard proposal={proposal("prop_new_task")} routerClient={client} onApproved={onApproved} />,
    );
    fireEvent.click(screen.getByText("Approve"));
    await waitFor(() => expect(onApproved).toHaveBeenCalled());
    const [approved] = onApproved.mock.calls[0];
    expect(approved.status).toBe("approved");
    expect(approved.applied_task_id).toBeTruthy();

    const createdTask = await client.getTask(approved.applied_task_id);
    expect(createdTask.title).toBe("Add a metrics counter for retry exhaustion");
  });

  it("dismisses via the Router client and reports the dismissed id", async () => {
    const client = new FakeRouterClient();
    const onDismissed = vi.fn();
    render(
      <ProposalCard
        proposal={proposal("prop_new_task")}
        routerClient={client}
        onDismissed={onDismissed}
      />,
    );
    fireEvent.click(screen.getByText("Dismiss"));
    await waitFor(() => expect(onDismissed).toHaveBeenCalledWith("prop_new_task"));

    const dismissed = await client.listProposals("session-active");
    expect(dismissed.find((p) => p.id === "prop_new_task")?.status).toBe("dismissed");
  });

  it("renders an already-approved proposal as a brief static notice, no actions", () => {
    render(
      <ProposalCard
        proposal={{ ...proposal("prop_new_task"), status: "approved" }}
        routerClient={new FakeRouterClient()}
      />,
    );
    expect(screen.getByText("Applied automatically")).toBeTruthy();
    expect(screen.queryByText("Approve")).toBeNull();
    expect(screen.queryByText("Dismiss")).toBeNull();
  });
});
