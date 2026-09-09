import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MergeBackFlow } from "./MergeBackFlow";
import { FakeRouterClient, fixtureConflictedMerge } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";

let nextSeq = 1;
function emitMerge(sessionId: string, type: string, payload: unknown) {
  const source = FakeEventSource.instances.find((s) => s.url.includes("merge.attempted"));
  act(() => {
    source?.emit(
      type,
      JSON.stringify({ seq: nextSeq++, type, actor: "user", session_id: sessionId, payload }),
    );
  });
}

describe("MergeBackFlow", () => {
  beforeEach(() => {
    nextSeq = 1;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("starts in the Not merged state", () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);
    expect(screen.getByText("Not merged")).toBeTruthy();
  });

  it("does not leak the placeholder dev note into the UI", () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);
    expect(screen.queryByText(/placeholder only/i)).toBeNull();
    expect(screen.getByLabelText("Target branch").getAttribute("placeholder")).toBe("main");
  });

  it("calls mergeSession with the typed target branch and shows Merging…", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);

    fireEvent.change(screen.getByLabelText("Target branch"), { target: { value: "main" } });
    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));

    expect(screen.getByText("Merging…")).toBeTruthy();
    await waitFor(() =>
      expect(FakeEventSource.instances.some((s) => s.url.includes("merge.attempted"))).toBe(true),
    );
  });

  it("reaches a Merged state once the control stream emits merge.attempted then merge.completed", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);

    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));
    await waitFor(() =>
      expect(FakeEventSource.instances.some((s) => s.url.includes("merge.attempted"))).toBe(true),
    );

    emitMerge("session-files-clean", "merge.attempted", { branch: "mads/tsk_retry-backoff" });
    emitMerge("session-files-clean", "merge.completed", { branch: "mads/tsk_retry-backoff" });

    await waitFor(() => expect(screen.getByText("Merged")).toBeTruthy());
  });

  it("surfaces ConflictResolution with the conflicted files once the control stream emits merge.conflicted", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-conflict" routerClient={client} />);

    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));
    await waitFor(() =>
      expect(FakeEventSource.instances.some((s) => s.url.includes("merge.attempted"))).toBe(true),
    );

    emitMerge("session-files-conflict", "merge.attempted", { branch: fixtureConflictedMerge.branch });
    emitMerge("session-files-conflict", "merge.conflicted", {
      branch: fixtureConflictedMerge.branch,
      files: fixtureConflictedMerge.files,
    });

    await waitFor(() => expect(screen.getByText("Merge conflicted")).toBeTruthy());
    expect(screen.getByText("Conflicted")).toBeTruthy();
    for (const path of fixtureConflictedMerge.files ?? []) {
      expect(screen.getByText(path)).toBeTruthy();
    }
  });

  it("shows an error status when mergeSession rejects", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "mergeSession").mockRejectedValue(new Error("boom"));
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);

    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));

    await waitFor(() => expect(screen.getByText("Merge failed")).toBeTruthy());
    expect(screen.getByText(/boom/)).toBeTruthy();
  });
});
