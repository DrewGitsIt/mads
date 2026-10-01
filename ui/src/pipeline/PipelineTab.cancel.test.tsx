import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PipelineTab } from "./PipelineTab";
import { FakeEventSource } from "../router/testFakeEventSource";
import { FakePipelineClient } from "./client/fixtures";
import { recordedSessionId, recordedTasks } from "./client/recordedRun";
import type { Envelope } from "../router/types";

const SID = recordedSessionId;
const env = (seq: number, type: string, payload: unknown = {}, task_id?: string): Envelope => ({
  seq,
  timestamp: new Date(Date.UTC(2026, 8, 30, 4, 21, seq % 60)).toISOString(),
  type,
  session_id: SID,
  actor: "pipeline",
  task_id,
  payload,
});

function emit(...envs: Envelope[]) {
  act(() => {
    for (const e of envs) {
      for (const s of FakeEventSource.instances) s.emit(e.type, JSON.stringify(e));
    }
  });
}

const startButton = () => screen.getByRole("button", { name: "Start" }) as HTMLButtonElement;

// The leader session's report: after cancelling a UI-launched run that a
// CLI run had overlapped, Start stayed dead until a remount. This replays
// that session's pipeline.* sequence (seqs from its log) into one mount.
describe("PipelineTab — Start after a cancelled, overlapped run", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("re-enables Start once the run it launched is cancelled, live", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={SID} pipelineClient={client} />);
    await waitFor(() => expect(startButton().disabled).toBe(false));

    fireEvent.click(startButton());
    emit(
      env(12226, "pipeline.started", { concurrency: 3 }),
      env(12231, "stage.started", { stage: "architect" }, "tsk_a"),
      env(12244, "pipeline.started", { concurrency: 1 }),
      env(12247, "stage.started", { stage: "architect" }, "tsk_b"),
    );
    await waitFor(() => expect(startButton().disabled).toBe(true));

    emit(env(12354, "pipeline.cancelled", { run_id: "run_789d8b08f61b635b" }));
    await waitFor(() => expect(startButton().disabled).toBe(false));
  });
});
