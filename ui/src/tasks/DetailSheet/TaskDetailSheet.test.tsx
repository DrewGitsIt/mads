// @vitest-environment jsdom
// Renders task description/acceptance_criteria through the shared Markdown
// renderer, which needs jsdom for DOMPurify (see ChatRenderer/markdown.test.tsx).
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskDetailSheet } from "./TaskDetailSheet";
import { FakeRouterClient, fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

function byId(id: string) {
  return fixtureTasks.find((t) => t.id === id)!;
}

describe("TaskDetailSheet", () => {
  it("renders every field editable, seeded from the task", () => {
    render(
      <TaskDetailSheet
        task={byId("tsk_ready")}
        tasksById={tasksById}
        sessionTasks={fixtureTasks}
        routerClient={new FakeRouterClient()}
        onClose={vi.fn()}
      />,
    );
      expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe(
      "Add retry backoff to the ingest worker",
    );
    expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("ready");
  });

  it("saves a full-replace PUT with edited fields on Save", async () => {
    const client = new FakeRouterClient();
    const onSaved = vi.fn();
    render(
      <TaskDetailSheet
        task={byId("tsk_ready")}
        tasksById={tasksById}
        sessionTasks={fixtureTasks}
        routerClient={client}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Add retry backoff, renamed" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const saved = onSaved.mock.calls[0][0];
    expect(saved.title).toBe("Add retry backoff, renamed");
    expect(saved.description).toBe(byId("tsk_ready").description);

    const fromServer = await client.getTask("tsk_ready");
    expect(fromServer.title).toBe("Add retry backoff, renamed");
  });

  it("groups notes by type and round-trips draft confirm/discard", async () => {
    const client = new FakeRouterClient();
    render(
      <TaskDetailSheet
        task={byId("tsk_draft")}
        tasksById={tasksById}
        sessionTasks={fixtureTasks}
        routerClient={client}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText(/Risk/)).toBeTruthy();
    expect(screen.getByText("Draft")).toBeTruthy();

    fireEvent.click(screen.getByText("Confirm"));
    await waitFor(() => expect(screen.queryByText("Draft")).toBeNull());
  });

  it("invokes onClose when Close is clicked", () => {
    const onClose = vi.fn();
    render(
      <TaskDetailSheet
        task={byId("tsk_ready")}
        tasksById={tasksById}
        sessionTasks={fixtureTasks}
        routerClient={new FakeRouterClient()}
        onClose={onClose}
      />,
    );
    fireEvent.click(screen.getByText("Close"));
    expect(onClose).toHaveBeenCalled();
  });
});
