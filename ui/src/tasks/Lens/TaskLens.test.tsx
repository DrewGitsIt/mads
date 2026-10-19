import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { TaskLens, type TaskLensProps } from "./TaskLens";
import { FakePipelineClient } from "../../pipeline/client/fixtures";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { StageAttempt, TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";

const task: Task = {
  id: "tsk_refresh",
  session_id: "ses_1",
  status: "in_progress",
  retry_count: 0,
  created_at: "2026-10-19T00:00:00Z",
  updated_at: "2026-10-19T00:00:00Z",
  title: "Refresh token rotation",
  labels: ["auth"],
  acceptance_criteria: "Reusing a rotated token revokes its whole family.",
  source: "user",
};

const stages: RunStage[] = [
  { name: "architect", kind: "agent" },
  { name: "executor", kind: "agent" },
  { name: "build", kind: "command" },
  { name: "code_review", kind: "agent", loopBack: "executor", maxLoops: 2, then: "block" },
  { name: "merge", kind: "command", when: '!contains(labels, "trivial")' },
];

const REVIEW_1 = "Reusing a rotated token doesn't revoke its siblings.";

const runningAttempts: StageAttempt[] = [
  { stage: "architect", attempt: 1, status: "completed", startedSeq: 1, endedSeq: 2 },
  { stage: "executor", attempt: 1, status: "completed", startedSeq: 3, endedSeq: 4 },
  { stage: "build", attempt: 1, status: "completed", startedSeq: 5, endedSeq: 6 },
  { stage: "code_review", attempt: 1, status: "failed", reason: REVIEW_1, startedSeq: 7, endedSeq: 8 },
  { stage: "executor", attempt: 2, status: "started", startedSeq: 9 },
];

function runState(attempts: StageAttempt[], touchedFiles: string[] = []): TaskRunState {
  return { taskId: task.id, currentStage: attempts[attempts.length - 1]?.stage, attempts, touchedFiles };
}

function setup(overrides: Partial<TaskLensProps> = {}) {
  const client = new FakePipelineClient([]);
  const props: TaskLensProps = {
    task,
    tasksById: { [task.id]: task },
    runStatus: "running",
    stages,
    runState: runState(runningAttempts, ["auth/refresh.go", "auth/store/token_store.go"]),
    sessionId: "ses_1",
    pipelineClient: client,
    runSeq: 7,
    live: true,
    onClose: vi.fn(),
    onOpenDetails: vi.fn(),
    onRetry: vi.fn(),
    onWontDo: vi.fn(),
    ...overrides,
  };
  const view = render(<TaskLens {...props} />);
  return { client, props, ...view };
}

function lastTarget(client: FakePipelineClient, kind: "output" | "turns") {
  return [...client.streamCalls].reverse().find((c) => c.kind === kind)?.target;
}

describe("TaskLens", () => {
  it("draws one station per stage with its state and sub-label", () => {
    setup();
    const strip = screen.getByRole("list", { name: "Stages" });
    const stations = within(strip).getAllByRole("button");
    expect(stations.map((b) => b.getAttribute("data-state"))).toEqual(["done", "now", "done", "fail", "wait"]);
    expect(screen.getByRole("button", { name: "executor: running, 2 attempts" }).textContent).toContain(
      "2 attempts",
    );
    expect(screen.getByRole("button", { name: "build: done" }).textContent).toContain("command");
    expect(screen.getByRole("button", { name: "merge: not reached" })).toHaveProperty("disabled", true);
  });

  it("marks a skipped stage and says why it has no output", () => {
    const attempts: StageAttempt[] = [
      ...runningAttempts.slice(0, 4),
      { stage: "merge", attempt: 1, status: "skipped", startedSeq: 10, endedSeq: 10 },
    ];
    const { client } = setup({ runState: runState(attempts), runStatus: "done" });
    const merge = screen.getByRole("button", { name: "merge: skipped" });
    expect(merge.textContent).toContain("skipped (when:)");
    expect(screen.getByText(/was false for this task/).textContent).toContain('when: !contains(labels, "trivial")');
    // A skipped attempt has no log to stream.
    expect(client.streamCalls).toEqual([]);
  });

  it("lists every attempt in the timeline with loop-back reasons", () => {
    setup();
    expect(screen.getAllByTestId("stage-timeline-entry")).toHaveLength(5);
    expect(screen.getByText(`↺ ${REVIEW_1}`)).toBeTruthy();
    expect(screen.getByRole("button", { name: "executor #2, started" })).toBeTruthy();
    // Status is a dot for the eye; screen readers get it in the name.
    expect(screen.getByRole("button", { name: "code_review, failed" })).toBeTruthy();
    expect(screen.queryByText("failed")).toBeNull();
  });

  it("opens the newest attempt's output and turns, scoped to the run", () => {
    const { client } = setup();
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "executor", attempt: 2 });
    expect(lastTarget(client, "turns")).toEqual({ run: 7, stage: "executor", attempt: 2 });
    expect(screen.getByTestId("handoff").textContent).toBe(
      `↳ Started from code_review attempt 1 feedback: “${REVIEW_1}”`,
    );
  });

  it("clicking a station opens that stage's latest attempt and closes the old streams", () => {
    const client = new FakePipelineClient([]);
    const closed: string[] = [];
    const original = client.streamTaskOutput.bind(client);
    client.streamTaskOutput = (sessionId, taskId, target) => {
      const source = original(sessionId, taskId, target);
      const close = source.close.bind(source);
      source.close = () => {
        closed.push(`${target?.stage}#${target?.attempt}`);
        close();
      };
      return source;
    };
    setup({ pipelineClient: client });

    fireEvent.click(screen.getByRole("button", { name: "architect: done" }));
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "architect", attempt: 1 });
    expect(lastTarget(client, "turns")).toEqual({ run: 7, stage: "architect", attempt: 1 });
    expect(closed).toContain("executor#2");
  });

  it("clicking a timeline entry opens that attempt", () => {
    const { client } = setup();
    fireEvent.click(screen.getByRole("button", { name: "code_review, failed" }));
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "code_review", attempt: 1 });
    expect(screen.getByText(/Passed to/).textContent).toBe("↳ Passed to executor attempt 2 as its feedback.");
  });

  it("switches between a stage's attempts with the attempt dropdown", () => {
    const { client } = setup();
    fireEvent.click(screen.getByRole("button", { name: /attempt 2 of 2 · live/ }));
    fireEvent.click(screen.getByRole("option", { name: "attempt 1 of 2" }));
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "executor", attempt: 1 });
    expect(screen.queryByTestId("handoff")).toBeNull();
  });

  it("the Raw log toggle reopens the attempt's log without a turns stream", () => {
    const { client } = setup();
    const before = client.streamCalls.length;
    fireEvent.click(screen.getByRole("radio", { name: "Raw log" }));
    expect(client.streamCalls.slice(before)).toEqual([
      { kind: "output", taskId: task.id, target: { run: 7, stage: "executor", attempt: 2 } },
    ]);
  });

  it("command stages have no Turns toggle and open only the log", () => {
    const { client } = setup();
    const before = client.streamCalls.length;
    fireEvent.click(screen.getByRole("button", { name: "build: done" }));
    expect(screen.queryByRole("radiogroup", { name: "Output view" })).toBeNull();
    expect(client.streamCalls.slice(before).map((c) => c.kind)).toEqual(["output"]);
  });

  it("follows a new attempt as it starts, unless an older one was picked", () => {
    const { client, props, rerender } = setup();
    const next: StageAttempt[] = [
      ...runningAttempts.slice(0, 4),
      { ...runningAttempts[4], status: "completed", endedSeq: 10 },
      { stage: "build", attempt: 2, status: "started", startedSeq: 11 },
    ];
    rerender(<TaskLens {...props} runState={runState(next)} />);
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "build", attempt: 2 });

    fireEvent.click(screen.getByRole("button", { name: "architect: done" }));
    const more = [...next, { stage: "code_review", attempt: 2, status: "started" as const, startedSeq: 12 }];
    rerender(<TaskLens {...props} runState={runState(more)} />);
    expect(lastTarget(client, "output")).toEqual({ run: 7, stage: "architect", attempt: 1 });
  });

  it("files touched open in Changes when the host can", () => {
    const onOpenFile = vi.fn();
    setup({ onOpenFile });
    fireEvent.click(screen.getByRole("button", { name: /auth\/refresh\.go/ }));
    expect(onOpenFile).toHaveBeenCalledWith("auth/refresh.go");
  });

  it("files touched are plain rows without onOpenFile", () => {
    setup();
    expect(screen.getByText("auth/refresh.go").closest("button")).toBeNull();
    expect(screen.queryByText("View in Changes ↗")).toBeNull();
  });

  it("shows the acceptance criteria and the header actions", () => {
    const { props } = setup();
    expect(screen.getByText(task.acceptance_criteria!)).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("auth")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Task details" }));
    expect(props.onOpenDetails).toHaveBeenCalledWith(task.id, "details");
    expect(screen.queryByText("Worktree diff")).toBeNull();
  });

  it("Esc and the close button close the lens", () => {
    const { props } = setup();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });

  it("drops the close hint when the host offers its own (graph focus), but Esc still closes", () => {
    const { props } = setup({ showCloseHint: false });
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("says why a queued task has no output yet", () => {
    const dep: Task = { ...task, id: "tsk_dep", title: "Token store", status: "in_progress" };
    const queued: Task = { ...task, status: "blocked", dependencies: [dep.id] };
    const { client } = setup({
      task: queued,
      tasksById: { [queued.id]: queued, [dep.id]: dep },
      runStatus: "queued",
      runState: undefined,
    });
    expect(screen.getByText("Queued: waits on Token store. Nothing to show until a stage starts.")).toBeTruthy();
    expect(screen.getByText("Not started.")).toBeTruthy();
    expect(client.streamCalls).toEqual([]);
  });

  describe("blocked", () => {
    const blockedAttempts: StageAttempt[] = [
      ...runningAttempts.slice(0, 4),
      { stage: "executor", attempt: 2, status: "completed", startedSeq: 9, endedSeq: 10 },
      { stage: "build", attempt: 2, status: "completed", startedSeq: 11, endedSeq: 12 },
      { stage: "code_review", attempt: 2, status: "failed", reason: "Still revokes only one.", startedSeq: 13, endedSeq: 14 },
    ];
    const blockedTask: Task = { ...task, status: "blocked" };

    function setupBlocked(overrides: Partial<TaskLensProps> = {}) {
      return setup({
        task: blockedTask,
        runStatus: "blocked",
        runState: runState(blockedAttempts),
        live: false,
        ...overrides,
      });
    }

    it("explains where and why it blocked", () => {
      setupBlocked();
      const box = screen.getByRole("group", { name: "Blocked" });
      expect(box.textContent).toContain("Blocked at code_review after 1 loop-back (then: block). Still revokes only one.");
    });

    it("prefers the host's block reason", () => {
      setupBlocked({ blockReason: "Needs a decision on impersonation." });
      expect(screen.getByRole("group", { name: "Blocked" }).textContent).toContain(
        "Needs a decision on impersonation.",
      );
    });

    it("wires its actions", () => {
      const onDiscuss = vi.fn();
      const { props } = setupBlocked({ onDiscuss });
      const box = within(screen.getByRole("group", { name: "Blocked" }));
      fireEvent.click(box.getByRole("button", { name: "Edit & retry" }));
      expect(props.onOpenDetails).toHaveBeenCalledWith(task.id, "retry");
      fireEvent.click(box.getByRole("button", { name: "Retry as is" }));
      expect(props.onRetry).toHaveBeenCalledWith(task.id);
      fireEvent.click(box.getByRole("button", { name: "Discuss in Conversation" }));
      expect(onDiscuss).toHaveBeenCalledWith(task.id);
      fireEvent.click(box.getByRole("button", { name: "Won't do" }));
      expect(props.onWontDo).toHaveBeenCalledWith(task.id);
    });

    it("hides Discuss without a handler", () => {
      setupBlocked();
      expect(screen.queryByRole("button", { name: "Discuss in Conversation" })).toBeNull();
    });

    it("a past run's failed attempt reads failed, not live", () => {
      setupBlocked();
      expect(screen.getByRole("button", { name: /attempt 2 of 2 · failed/ })).toBeTruthy();
    });
  });
});
