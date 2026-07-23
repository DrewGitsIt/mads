import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ContextRail } from "./ContextRail";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

describe("ContextRail", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the Tasks tab by default with quick-filter chips", () => {
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    expect(screen.getByRole("button", { name: "All" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "In progress" })).toBeTruthy();
    expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy();
  });

  it("renders fetched synthesis proposals above the tab switcher, regardless of active tab", async () => {
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    await waitFor(() =>
      expect(screen.getByText("Add a metrics counter for retry exhaustion")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("Changes"));
    expect(screen.getByText("Add a metrics counter for retry exhaustion")).toBeTruthy();
  });

  it("a quick-filter chip cuts the list to a single status", () => {
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.getByText("Add retry metrics dashboard")).toBeTruthy();
    expect(screen.queryByText("Write regression test for the retry race")).toBeNull();
  });

  it("switches to the Changes tab and renders file changes", async () => {
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    fireEvent.click(screen.getByText("Changes"));
    await waitFor(() =>
      expect(screen.getByText("services/data/internal/ingest.go")).toBeTruthy(),
    );
  });

  it("collapses and expands", () => {
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
      />,
    );
    fireEvent.click(screen.getByLabelText("Collapse context rail"));
    expect(screen.queryByText("Tasks")).toBeNull();
    fireEvent.click(screen.getByLabelText("Expand context rail"));
    expect(screen.getByText("Tasks")).toBeTruthy();
  });

  it("invokes onOpenFile from the Changes tab", async () => {
    const onOpenFile = vi.fn();
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
        onOpenFile={onOpenFile}
      />,
    );
    fireEvent.click(screen.getByText("Changes"));
    await waitFor(() =>
      expect(screen.getByText("services/data/internal/ingest.go")).toBeTruthy(),
    );
    fireEvent.click(screen.getByText("services/data/internal/ingest.go"));
    expect(onOpenFile).toHaveBeenCalledWith("services/data/internal/ingest.go");
  });
});
