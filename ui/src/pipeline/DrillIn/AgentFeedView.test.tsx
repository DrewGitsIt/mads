import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { AgentFeedView } from "./AgentFeedView";
import type { Task, TranscriptMessage } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";

const task = { id: "t1", title: "add login endpoint" } as Task;
const attempts: StageAttempt[] = [{ stage: "executor", attempt: 1, status: "started", startedSeq: 1 }];

// Fixtures mirror providers/claudecode/testdata/transcript.jsonl (a real
// recorded transcript, post provider-normalization). `turns[0]` is always the
// engine's injected stage prompt (engine.go's taskPrompt) for a single stage
// attempt's transcript — whatever its text says, which is exactly why
// AgentFeedView detects it by position rather than sniffing that text.
const stagePromptTurn: TranscriptMessage = {
  role: "user",
  text: "add a login endpoint",
  timestamp: "2026-07-11T21:59:23Z",
};
const agentTurn: TranscriptMessage = {
  role: "agent",
  text: "I'll add the endpoint now.",
  timestamp: "2026-07-11T21:59:24Z",
};
const editTool: TranscriptMessage = {
  role: "tool",
  tool: { id: "toolu_01", name: "Edit", path: "api/login.go", input: { file_path: "api/login.go" } },
  timestamp: "2026-07-11T21:59:25Z",
};
const editResult: TranscriptMessage = {
  role: "tool_result",
  text: "ok",
  tool: { id: "toolu_01", name: "" },
  timestamp: "2026-07-11T21:59:26Z",
};
const genuineUserTurn: TranscriptMessage = {
  role: "user",
  text: "looks good, run the tests",
  timestamp: "2026-07-11T21:59:30Z",
};
const bashTool: TranscriptMessage = {
  role: "tool",
  tool: { id: "toolu_02", name: "Bash", input: { command: "go test ./..." } },
  timestamp: "2026-07-11T21:59:31Z",
};
const finalAgentTurn: TranscriptMessage = {
  role: "agent",
  text: "Tests pass.",
  timestamp: "2026-07-11T21:59:40Z",
};

// Mirrors providers/claudecode/testdata/transcript_denial.jsonl — a real
// recorded permission denial.
const deniedTool: TranscriptMessage = {
  role: "tool",
  tool: { id: "toolu_11", name: "Read", path: "docs/screenshot.png" },
  timestamp: "2026-07-19T10:00:01Z",
};
const deniedResult: TranscriptMessage = {
  role: "tool_result",
  text: "permission denied: image reads require approval",
  is_error: true,
  tool: { id: "toolu_11", name: "" },
  timestamp: "2026-07-19T10:00:02Z",
};

describe("AgentFeedView", () => {
  // Fixed, non-UTC-by-default TZ so every assertion below is independent of
  // the host running the suite. individual tests override it further to
  // prove local-time rendering rather than a hardcoded UTC passthrough.
  const originalTz = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = "UTC";
  });
  afterAll(() => {
    process.env.TZ = originalTz;
  });

  it("labels the stage-opener as system, distinct from a genuine user turn and an agent turn", () => {
    render(
      <AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, agentTurn, genuineUserTurn]} />,
    );

    const proseRows = screen.getAllByTestId("feed-prose-row");
    expect(proseRows).toHaveLength(3);
    expect(proseRows[0].dataset.role).toBe("system");
    expect(proseRows[0].textContent).toContain("system");
    expect(proseRows[1].dataset.role).toBe("agent");
    expect(proseRows[2].dataset.role).toBe("user");
    expect(proseRows[2].textContent).toContain("user");
  });

  it("renders a tool call as a structured card with its folded result, not a separate result row", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, editTool, editResult]} />);

    const toolRow = screen.getByTestId("feed-tool-row");
    expect(toolRow.textContent).toContain("Edit");
    expect(toolRow.textContent).toContain("api/login.go");
    expect(toolRow.textContent).toContain("ok");
    expect(screen.queryByTestId("feed-tool-result-row")).toBeNull();
  });

  it("shows a failing tool result as visibly distinct, collapsed by default with an expand affordance", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, deniedTool, deniedResult]} />);

    const toolRow = screen.getByTestId("feed-tool-row");
    expect(toolRow.textContent).toContain("Read");
    expect(toolRow.textContent).toContain("error");
    // collapsed: the full denial text isn't in the DOM yet
    expect(screen.queryByText(/permission denied/)).toBeNull();

    fireEvent.click(within(toolRow).getByRole("button"));

    expect(screen.getByText(/permission denied/)).toBeTruthy();
    expect(toolRow.querySelector("[data-error]")).toBeTruthy();
  });

  it("a server-side tool event with no local result renders a card with no status, not raw text", () => {
    const serverToolUse: TranscriptMessage = {
      role: "tool",
      tool: { id: "srv_1", name: "web_search", input: { query: "mads pipeline" } },
      timestamp: "2026-07-11T21:59:32Z",
    };
    render(
      <AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, serverToolUse, finalAgentTurn]} />,
    );

    const toolRow = screen.getByTestId("feed-tool-row");
    expect(toolRow.textContent).toContain("web_search");
    expect(toolRow.dataset.pending).toBeUndefined(); // not the trailing turn, so not "running"
    expect(screen.queryByText("ok")).toBeNull();
    expect(screen.queryByText("error")).toBeNull();
  });

  it("splits an interleaved text/tool/text agent message into three ordered rows, each keeping its own timestamp", () => {
    render(
      <AgentFeedView
        task={task}
        attempts={attempts}
        turns={[stagePromptTurn, agentTurn, editTool, editResult, finalAgentTurn]}
      />,
    );

    const rows = Array.from(screen.getByTestId("feed-rows").children) as HTMLElement[];
    // stage prompt, agent text, tool card, agent text — never merged into one blob
    expect(rows).toHaveLength(4);
    expect(rows[1].dataset.testid).toBe("feed-prose-row");
    expect(rows[1].textContent).toContain("I'll add the endpoint now.");
    expect(rows[1].textContent).toContain("21:59:24");
    expect(rows[2].dataset.testid).toBe("feed-tool-row");
    expect(rows[2].textContent).toContain("21:59:25");
    expect(rows[3].dataset.testid).toBe("feed-prose-row");
    expect(rows[3].textContent).toContain("Tests pass.");
    expect(rows[3].textContent).toContain("21:59:40");
  });

  it("shows the Bash command as the tool row's salient argument", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[bashTool]} />);
    expect(screen.getByTestId("feed-tool-row").textContent).toContain("go test ./...");
  });

  it("shows a visible timestamp on every row, in the viewer's local timezone", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, bashTool]} />);
    expect(screen.getByText("21:59:23")).toBeTruthy();
    expect(screen.getByText("21:59:31")).toBeTruthy();
  });

  it("renders feed and header timestamps in the viewer's local timezone, not UTC", () => {
    process.env.TZ = "Asia/Kolkata"; // UTC+5:30 — deliberately not UTC or the CI host's zone
    try {
      render(<AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn, bashTool]} />);

      // 21:59:23Z / 21:59:31Z + 5:30 rolls into the next day
      expect(screen.getByTestId("feed-header").textContent).toContain("03:29:23");
      expect(screen.queryByText("21:59:23")).toBeNull();
      expect(screen.getByText("03:29:23")).toBeTruthy();
      expect(screen.getByText("03:29:31")).toBeTruthy();
    } finally {
      process.env.TZ = "UTC";
    }
  });

  it("shows the current stage/attempt and task in the header", () => {
    render(<AgentFeedView task={task} attempts={attempts} turns={[stagePromptTurn]} />);
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
});
