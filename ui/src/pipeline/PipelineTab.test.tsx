import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { PipelineTab } from "./PipelineTab";
import { FakeEventSource } from "../router/testFakeEventSource";
import { FakePipelineClient } from "./client/fixtures";
import { recordedEnvelopes, recordedSessionId, recordedTasks } from "./client/recordedRun";

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("PipelineTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("composes the run header, board, and blocked panel from the recorded run", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();

    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());
    expect(screen.getByText("Done")).toBeTruthy();
    // RunHeader's count chip and BlockedPanel's heading chip both say "1 blocked".
    expect(screen.getAllByText("1 blocked").length).toBe(2);
    // core lib appears once, in Done — flaky api is excluded from the board
    // columns and appears only in BlockedPanel.
    expect(screen.getAllByText("core lib").length).toBe(1);
  });

  it("opens the drill-in when a task card is clicked, and closes it", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();
    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "core lib" }));
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );

    fireEvent.click(screen.getByRole("button", { name: "close" }));
    expect(document.querySelector('[data-testid="terminal-view"]')).toBeNull();
  });

  it("opens the drill-in from a blocked task's card and shows its feedback", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();
    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());

    fireEvent.click(screen.getByText("flaky api").closest("button")!);
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
    expect(screen.getAllByText("code_review").length).toBeGreaterThan(0);
  });
});
