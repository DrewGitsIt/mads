import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MergeBackFlow } from "./MergeBackFlow";
import { FakeRouterClient, fixtureConflictedMerge } from "../../router/fixtures";

describe("MergeBackFlow", () => {
  it("starts in the Not merged state", () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);
    expect(screen.getByText("Not merged")).toBeTruthy();
  });

  it("calls mergeSession with the typed target branch and shows Merging…", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);

    fireEvent.change(screen.getByLabelText("Target branch"), { target: { value: "main" } });
    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));

    expect(screen.getByText("Merging…")).toBeTruthy();
    await waitFor(() => expect(client.controlEventSources.length).toBe(1));
  });

  it("reaches a Merged state once the control stream emits merge.attempted then merge.completed", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-clean" routerClient={client} />);

    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));
    await waitFor(() => expect(client.controlEventSources.length).toBe(1));
    const source = client.controlEventSources[0];

    source.emit("merge.attempted", {
      session_id: "session-files-clean",
      payload: { branch: "mads/tsk_retry-backoff" },
    });
    source.emit("merge.completed", {
      session_id: "session-files-clean",
      payload: { branch: "mads/tsk_retry-backoff" },
    });

    await waitFor(() => expect(screen.getByText("Merged")).toBeTruthy());
  });

  it("surfaces ConflictResolution with the conflicted files once the control stream emits merge.conflicted", async () => {
    const client = new FakeRouterClient();
    render(<MergeBackFlow sessionId="session-files-conflict" routerClient={client} />);

    fireEvent.click(screen.getByRole("button", { name: "Merge back" }));
    await waitFor(() => expect(client.controlEventSources.length).toBe(1));
    const source = client.controlEventSources[0];

    source.emit("merge.attempted", {
      session_id: "session-files-conflict",
      payload: { branch: fixtureConflictedMerge.branch },
    });
    source.emit("merge.conflicted", {
      session_id: "session-files-conflict",
      payload: { branch: fixtureConflictedMerge.branch, files: fixtureConflictedMerge.files },
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
