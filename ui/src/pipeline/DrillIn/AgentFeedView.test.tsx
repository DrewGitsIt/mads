import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { AgentFeedView } from "./AgentFeedView";
import type { Task, TranscriptMessage } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";

const task = { id: "t1", title: "add login endpoint" } as Task;
const attempts: StageAttempt[] = [{ stage: "executor", attempt: 1, status: "started", startedSeq: 1 }];

const userTurn: TranscriptMessage = {
  role: "user",
  text: "add a login endpoint",
  timestamp: "2026-07-11T21:59:23Z",
};
const agentTurn: TranscriptMessage = {
  role: "agent",
  text: "I'll add the endpoint now.",
  timestamp: "2026-07-11T21:59:24Z",
};
const bashTool: TranscriptMessage = {
  role: "tool",
  tool: { id: "toolu_02", name: "Bash", input: { command: "go test ./..." } },
  timestamp: "2026-07-11T21:59:31Z",
};
const editTool: TranscriptMessage = {
  role: "tool",
  tool: { id: "toolu_01", name: "Edit", path: "api/login.go" },
  timestamp: "2026-07-11T21:59:25Z",
};
const editResult: TranscriptMessage = {
  role: "tool_result",
  text: "ok",
  tool: { id: "toolu_01", name: "" },
  timestamp: "2026-07-11T21:59:26Z",
};

describe("AgentFeedView", () => {
  it("renders prose rows for user/agent turns and tool rows for tool turns, distinctly", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[userTurn, agentTurn, editTool, editResult]} />);

    const proseRows = screen.getAllByTestId("feed-prose-row");
    expect(proseRows).toHaveLength(2);
    expect(proseRows[0].dataset.role).toBe("user");
    expect(proseRows[1].dataset.role).toBe("agent");

    expect(screen.getByTestId("feed-tool-row").textContent).toContain("Edit");
    expect(screen.getByTestId("feed-tool-row").textContent).toContain("api/login.go");
    expect(screen.getByTestId("feed-tool-result-row")).toBeTruthy();
  });

  it("shows the Bash command as the tool row's salient argument", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[bashTool]} />);
    expect(screen.getByTestId("feed-tool-row").textContent).toContain("go test ./...");
  });

  it("shows a visible timestamp on every row", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[userTurn, bashTool]} />);
    expect(screen.getByText("21:59:23")).toBeTruthy();
    expect(screen.getByText("21:59:31")).toBeTruthy();
  });

  it("shows the current stage/attempt and task in the header", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[userTurn]} />);
    const header = screen.getByTestId("feed-header");
    expect(header.textContent).toContain("add login endpoint");
    expect(header.textContent).toContain("executor");
    expect(header.textContent).toContain("attempt 1");
  });

  describe("liveness", () => {
    beforeEach(() => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-07-11T21:59:41Z"));
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it("marks a trailing tool_use with no tool_result as pending and ticks its elapsed time", () => {
      render(<AgentFeedView task={task} attempts={attempts} turns={[editTool, editResult, bashTool]} />);

      const toolRows = screen.getAllByTestId("feed-tool-row");
      const pendingRow = toolRows.find((row) => row.dataset.pending === "true");
      expect(pendingRow).toBeTruthy();
      expect(screen.getByTestId("feed-liveness").textContent).toBe("running 10s");

      act(() => vi.advanceTimersByTime(3000));
      expect(screen.getByTestId("feed-liveness").textContent).toBe("running 13s");
    });

    it("shows no liveness indicator once the tool_use has a matching tool_result", () => {
      render(<AgentFeedView task={task} attempts={attempts} turns={[editTool, editResult]} />);
      expect(screen.queryByTestId("feed-liveness")).toBeNull();
    });
  });

  it("middle-truncates a long argument and expands it on click", () => {
    const longPath = "a/" + "b".repeat(100) + "/c.go";
    const longTool: TranscriptMessage = {
      role: "tool",
      tool: { id: "t1", name: "Read", path: longPath },
    };
    render(<AgentFeedView task={task} attempts={attempts} turns={[longTool]} />);

    const truncatable = screen.getByTestId("feed-truncatable");
    expect(truncatable.textContent).not.toBe(longPath);
    expect(truncatable.textContent!.length).toBeLessThan(longPath.length);

    fireEvent.click(truncatable);
    expect(truncatable.textContent).toBe(longPath);
  });
});
