import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskDraftCard } from "./TaskDraftCard";
import { FakeRouterClient, fixtureTasks } from "../../router/fixtures";

function draftTask() {
  return fixtureTasks.find((t) => t.id === "tsk_draft")!;
}

describe("TaskDraftCard", () => {
  it("renders the kit Draft provenance treatment", () => {
    render(
      <TaskDraftCard task={draftTask()} routerClient={new FakeRouterClient()} onEdit={vi.fn()} />,
    );
    expect(screen.getByText("Draft")).toBeTruthy();
    expect(screen.getByText("Add circuit breaker around the ingest retry loop")).toBeTruthy();
  });

  it("confirms via the Router client and reports the confirmed task", async () => {
    const client = new FakeRouterClient();
    const onConfirmed = vi.fn();
    render(
      <TaskDraftCard
        task={draftTask()}
        routerClient={client}
        onEdit={vi.fn()}
        onConfirmed={onConfirmed}
      />,
    );
    fireEvent.click(screen.getByText("Confirm"));
    await waitFor(() => expect(onConfirmed).toHaveBeenCalled());

    const fromServer = await client.getTask("tsk_draft");
    expect(fromServer.draft).toBe(false);
  });

  it("discards via the Router client and reports the discarded id", async () => {
    const client = new FakeRouterClient();
    const onDiscarded = vi.fn();
    render(
      <TaskDraftCard
        task={draftTask()}
        routerClient={client}
        onEdit={vi.fn()}
        onDiscarded={onDiscarded}
      />,
    );
    fireEvent.click(screen.getByText("Discard"));
    await waitFor(() => expect(onDiscarded).toHaveBeenCalledWith("tsk_draft"));

    await expect(client.getTask("tsk_draft")).rejects.toThrow();
  });

  it("invokes onEdit without confirming", () => {
    const onEdit = vi.fn();
    const onConfirmed = vi.fn();
    render(
      <TaskDraftCard
        task={draftTask()}
        routerClient={new FakeRouterClient()}
        onEdit={onEdit}
        onConfirmed={onConfirmed}
      />,
    );
    fireEvent.click(screen.getByText("Edit"));
    expect(onEdit).toHaveBeenCalledWith(draftTask());
    expect(onConfirmed).not.toHaveBeenCalled();
  });
});
