import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { RailChangesList } from "./RailChangesList";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";
import type { FileChange } from "../router/types";

describe("RailChangesList", () => {
  it("renders A/M/D badges and attribution for a session's file changes", async () => {
    const client = new FakeRouterClient();
    render(<RailChangesList sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("services/data/internal/ingest.go")).toBeTruthy(),
    );
    expect(screen.getByText("services/data/internal/ingest_test.go")).toBeTruthy();
    expect(screen.getAllByText("autonomous-executor").length).toBe(2);
  });

  it("badges an unattributed change distinctly", async () => {
    const unattributed: FileChange = {
      id: 99,
      session_id: "session-active",
      path: "README.md",
      change: "deleted",
      origin: "observed",
      attributed: false,
      timestamp: "2026-07-01T12:00:00Z",
    };
    const client = new FakeRouterClient(
      undefined,
      undefined,
      undefined,
      undefined,
      fixtureTasks,
      { "session-active": [unattributed] },
    );
    render(<RailChangesList sessionId="session-active" routerClient={client} />);

    await waitFor(() => expect(screen.getByText("README.md")).toBeTruthy());
    expect(screen.getByText("unattributed")).toBeTruthy();
  });

  it("invokes onOpenFile with the clicked path", async () => {
    const onOpenFile = vi.fn();
    const client = new FakeRouterClient();
    render(
      <RailChangesList sessionId="session-active" routerClient={client} onOpenFile={onOpenFile} />,
    );

    await waitFor(() =>
      expect(screen.getByText("services/data/internal/ingest.go")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("services/data/internal/ingest.go"));
    expect(onOpenFile).toHaveBeenCalledWith("services/data/internal/ingest.go");
  });
});
