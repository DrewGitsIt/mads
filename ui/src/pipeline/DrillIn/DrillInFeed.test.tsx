import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { DrillInFeed } from "./DrillInFeed";
import type { Task, TranscriptMessage } from "../../router/types";
import type { PipelineOutputSource, PipelineTurnsSource } from "../client/pipelineClient";
import type { StageAttempt } from "../state/usePipelineRun";

const task = { id: "t1", title: "add login endpoint" } as Task;
const attempts: StageAttempt[] = [];

function makeOutputSource(): PipelineOutputSource {
  return { onChunk: vi.fn(() => () => {}), close: vi.fn() };
}

function makeTurnsSource() {
  const listeners = new Set<(turns: TranscriptMessage[]) => void>();
  const source: PipelineTurnsSource = {
    onTurns: vi.fn((listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }),
    close: vi.fn(),
  };
  return { source, emit: (turns: TranscriptMessage[]) => listeners.forEach((l) => l(turns)) };
}

describe("DrillInFeed", () => {
  it("renders the raw output view when no turns ever arrive", async () => {
    const outputSource = makeOutputSource();
    const { source: turnsSource } = makeTurnsSource();

    render(
      <DrillInFeed task={task} attempts={attempts} outputSource={outputSource} turnsSource={turnsSource} />,
    );

    await waitFor(() => expect(outputSource.onChunk).toHaveBeenCalled());
    expect(screen.queryByTestId("agent-feed")).toBeNull();
  });

  it("switches to the normalized turns feed once a non-empty turns frame arrives", async () => {
    const outputSource = makeOutputSource();
    const { source: turnsSource, emit } = makeTurnsSource();

    render(
      <DrillInFeed task={task} attempts={attempts} outputSource={outputSource} turnsSource={turnsSource} />,
    );
    await waitFor(() => expect(turnsSource.onTurns).toHaveBeenCalled());

    emit([{ role: "user", text: "hi", timestamp: "2026-07-11T21:59:23Z" }]);

    expect(await screen.findByTestId("agent-feed")).toBeTruthy();
  });
});
