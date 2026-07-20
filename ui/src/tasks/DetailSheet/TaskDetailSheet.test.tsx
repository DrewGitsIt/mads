// @vitest-environment jsdom
// Renders task description/acceptance_criteria through the shared Markdown
// renderer, which needs jsdom for DOMPurify (see ChatRenderer/markdown.test.tsx).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskDetailSheet } from "./TaskDetailSheet";
import { FakeRouterClient, fixtureTasks } from "../../router/fixtures";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import { recordedEnvelopes, recordedSessionId, recordedTaskIds, recordedTasks } from "../../pipeline/client/recordedRun";

// jsdom (unlike the suite's default happy-dom) has no matchMedia — usePrefersReducedMotion
// needs one, same stub shape as GraphCanvas.test.tsx.
vi.stubGlobal("matchMedia", (query: string) => ({
  matches: false,
  media: query,
  addEventListener: () => {},
  removeEventListener: () => {},
}));

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const recordedTasksById = Object.fromEntries(recordedTasks.map((t) => [t.id, t]));

function byId(id: string) {
  return fixtureTasks.find((t) => t.id === id)!;
}

function byRecordedId(id: string) {
  return recordedTasks.find((t) => t.id === id)!;
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

  describe("click-to-edit long-text fields", () => {
    it("shows only the rendered preview at rest, no textarea, for a populated field", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      expect(screen.getByText("Exponential backoff with jitter, capped at 30s.")).toBeTruthy();
      expect(screen.queryByRole("textbox", { name: "Description" })).toBeNull();
    });

    it("shows a clickable placeholder for an empty field", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      expect(screen.getByText("Add scope…")).toBeTruthy();
      const scopeField = screen.getByRole("button", { name: "Edit Scope" });
      fireEvent.click(scopeField);
      expect((screen.getByRole("textbox", { name: "Scope" }) as HTMLTextAreaElement).value).toBe(
        "",
      );
    });

    it("switches to an autofocused textarea with the raw text on click", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
      const textarea = screen.getByRole("textbox", { name: "Description" }) as HTMLTextAreaElement;
      expect(textarea.value).toBe("Exponential backoff with jitter, capped at 30s.");
      expect(document.activeElement).toBe(textarea);
    });

    it("begins editing on Enter when the preview is keyboard-focused", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      fireEvent.keyDown(screen.getByRole("button", { name: "Edit Description" }), {
        key: "Enter",
      });
      expect(screen.getByRole("textbox", { name: "Description" })).toBeTruthy();
    });

    it("commits the edit to the draft on blur", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
      const textarea = screen.getByRole("textbox", { name: "Description" });
      fireEvent.change(textarea, { target: { value: "Retry with jitter, revised." } });
      fireEvent.blur(textarea);

      expect(screen.queryByRole("textbox", { name: "Description" })).toBeNull();
      expect(screen.getByText("Retry with jitter, revised.")).toBeTruthy();
    });

    it("commits the edit on Cmd/Ctrl+Enter", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
      const textarea = screen.getByRole("textbox", { name: "Description" });
      fireEvent.change(textarea, { target: { value: "Committed via Cmd+Enter." } });
      fireEvent.keyDown(textarea, { key: "Enter", metaKey: true });

      expect(screen.queryByRole("textbox", { name: "Description" })).toBeNull();
      expect(screen.getByText("Committed via Cmd+Enter.")).toBeTruthy();
    });

    it("discards the in-flight edit on Escape, leaving the draft unchanged", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
      const textarea = screen.getByRole("textbox", { name: "Description" });
      fireEvent.change(textarea, { target: { value: "This should never be saved." } });
      fireEvent.keyDown(textarea, { key: "Escape" });

      expect(screen.queryByRole("textbox", { name: "Description" })).toBeNull();
      expect(screen.queryByText("This should never be saved.")).toBeNull();
      expect(screen.getByText("Exponential backoff with jitter, capped at 30s.")).toBeTruthy();
    });

    it("saves the committed edit through the existing draft/save plumbing", async () => {
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
      fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
      const textarea = screen.getByRole("textbox", { name: "Description" });
      fireEvent.change(textarea, { target: { value: "Saved via draft plumbing." } });
      fireEvent.blur(textarea);
      fireEvent.click(screen.getByText("Save"));

      await waitFor(() => expect(onSaved).toHaveBeenCalled());
      expect(onSaved.mock.calls[0][0].description).toBe("Saved via draft plumbing.");
    });
  });

  describe("pipeline run section", () => {
    beforeEach(() => {
      FakeEventSource.instances = [];
      vi.stubGlobal("EventSource", FakeEventSource);
    });

    afterEach(() => {
      vi.unstubAllGlobals();
      // vi.unstubAllGlobals also clears the module-level matchMedia stub
      // above (usePrefersReducedMotion needs it on every render) — restore it.
      vi.stubGlobal("matchMedia", (query: string) => ({
        matches: false,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }));
    });

    it("renders no Pipeline run section when pipelineClient is omitted", () => {
      render(
        <TaskDetailSheet
          task={byId("tsk_ready")}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
        />,
      );
      expect(screen.queryByText("Pipeline run")).toBeNull();
    });

    it("shows the stage timeline and current stage for a task with pipeline history", async () => {
      const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
      client.runs = [
        {
          session_id: recordedSessionId,
          run_seq: 1,
          started_at: "2026-07-15T05:30:12.262609722Z",
          ended_at: "2026-07-15T05:30:12.28113183Z",
          outcome: "incomplete",
          tasks_done: 1,
          tasks_blocked: 1,
          tasks_skipped: 1,
          cost_usd: 0,
        },
      ];
      client.runEvents = recordedEnvelopes;

      render(
        <TaskDetailSheet
          task={byRecordedId(recordedTaskIds.coreLib)}
          tasksById={recordedTasksById}
          sessionTasks={recordedTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
          pipelineClient={client}
        />,
      );

      await waitFor(() => expect(screen.getByTestId("stage-timeline")).toBeTruthy());
      // "verify" also appears as a stage name inside the timeline itself —
      // assert at least the current-stage line and the timeline both show it.
      expect(screen.getAllByText("verify").length).toBeGreaterThan(1);
    });

    it("shows the blocker note and a working Retry button for a blocked task", async () => {
      const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
      const blocked = byRecordedId(recordedTaskIds.flakyApi);
      let retriedId: string | undefined;
      const retryTask = client.retryTask.bind(client);
      client.retryTask = async (taskId: string) => {
        retriedId = taskId;
        return retryTask(taskId);
      };

      render(
        <TaskDetailSheet
          task={blocked}
          tasksById={recordedTasksById}
          sessionTasks={recordedTasks}
          routerClient={new FakeRouterClient()}
          onClose={vi.fn()}
          pipelineClient={client}
        />,
      );

      // The blocker note is intentionally duplicated: once in the Pipeline
      // run section's BlockedTaskFeedback, once in the generic NoteGroup
      // "Blocker" section further down (HANDOFF.md).
      expect(screen.getAllByText(/stage code_review: /).length).toBeGreaterThan(0);
      fireEvent.click(screen.getByRole("button", { name: "Retry" }));

      await waitFor(() => expect(retriedId).toBe(blocked.id));
    });
  });
});
