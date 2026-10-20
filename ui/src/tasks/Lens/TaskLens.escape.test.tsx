// @vitest-environment jsdom
// TaskDetailSheet renders Markdown, which needs jsdom for DOMPurify.
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TaskLens } from "./TaskLens";
import { TaskDetailSheet } from "../DetailSheet/TaskDetailSheet";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
import { FakeRouterClient, fixtureTasks } from "../../router/fixtures";

vi.stubGlobal("matchMedia", (query: string) => ({
  matches: false,
  media: query,
  addEventListener: () => {},
  removeEventListener: () => {},
}));

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const task = fixtureTasks.find((t) => t.id === "tsk_ready")!;

function Harness({ onCloseLens }: { onCloseLens: () => void }) {
  const [sheet, setSheet] = useState(false);
  return (
    <>
      <TaskLens
        task={task}
        tasksById={tasksById}
        runStatus="queued"
        stages={[{ name: "executor", kind: "agent" }]}
        sessionId={task.session_id}
        pipelineClient={new FakePipelineClient([])}
        live
        onClose={onCloseLens}
        onOpenDetails={() => setSheet(true)}
        onRetry={vi.fn()}
        onWontDo={vi.fn()}
      />
      {sheet && (
        <TaskDetailSheet
          task={task}
          tasksById={tasksById}
          sessionTasks={fixtureTasks}
          routerClient={new FakeRouterClient()}
          onClose={() => setSheet(false)}
        />
      )}
    </>
  );
}

describe("Esc layers: lens and detail sheet", () => {
  it("closes the sheet first, then the lens", () => {
    const onCloseLens = vi.fn();
    render(<Harness onCloseLens={onCloseLens} />);
    fireEvent.click(screen.getByRole("button", { name: "Task details" }));
    expect(screen.getByLabelText("Title")).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByLabelText("Title")).toBeNull();
    expect(onCloseLens).not.toHaveBeenCalled();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCloseLens).toHaveBeenCalledTimes(1);
  });

  it("an in-field edit's Escape cancels the edit only", () => {
    const onCloseLens = vi.fn();
    render(<Harness onCloseLens={onCloseLens} />);
    fireEvent.click(screen.getByRole("button", { name: "Task details" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit Description" }));
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Description" }), { key: "Escape" });
    expect(screen.getByLabelText("Title")).toBeTruthy();
    expect(onCloseLens).not.toHaveBeenCalled();
  });
});
