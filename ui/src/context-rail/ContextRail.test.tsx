import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ContextRail } from "./ContextRail";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const openTaskCount = fixtureTasks.filter((t) => t.status !== "done" && t.status !== "wont_do").length;

function expand() {
  fireEvent.click(screen.getByLabelText("Expand context rail"));
}

function renderRail() {
  return render(
    <ContextRail
      sessionId="session-active"
      routerClient={new FakeRouterClient()}
      tasks={fixtureTasks}
      tasksById={tasksById}
    />,
  );
}

describe("ContextRail", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("starts collapsed, with the open-task and changed-file counts on the strip", async () => {
    renderRail();
    expect(screen.queryByRole("button", { name: "All" })).toBeNull();
    expect(screen.getByLabelText(`Tasks, ${openTaskCount} open`)).toBeTruthy();
    await waitFor(() => expect(screen.getByLabelText(/^Changes, [1-9]\d* files$/)).toBeTruthy());
    await waitFor(() => expect(screen.getByLabelText(/pending proposals$/)).toBeTruthy());
  });

  it("clicking anywhere on the collapsed strip expands to the Tasks tab", () => {
    const { container } = renderRail();
    fireEvent.click(container.firstElementChild!);
    expect(screen.getByRole("button", { name: "All" })).toBeTruthy();
  });

  it("the collapsed strip's Changes label opens straight to Changes", async () => {
    renderRail();
    await waitFor(() => expect(screen.getByLabelText(/^Changes, [1-9]/)).toBeTruthy());
    fireEvent.click(screen.getByLabelText(/^Changes, /));
    await waitFor(() => expect(screen.getByText("ingest.go")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "All" })).toBeNull();
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
    expand();
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
    expand();
    await waitFor(() =>
      expect(screen.getByText("Add a metrics counter for retry exhaustion")).toBeTruthy(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Changes" }));
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
    expand();
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
    expand();
    fireEvent.click(screen.getByRole("button", { name: "Changes" }));
    await waitFor(() => expect(screen.getByText("ingest.go")).toBeTruthy());
  });

  it("collapses and expands", () => {
    renderRail();
    expand();
    fireEvent.click(screen.getByLabelText("Collapse context rail"));
    expect(screen.queryByRole("button", { name: "All" })).toBeNull();
    expand();
    expect(screen.getByRole("button", { name: "All" })).toBeTruthy();
  });

  it("counts changed files from the change set, leaving scratch out", async () => {
    const client = new FakeRouterClient();
    render(
      <ContextRail sessionId="session-files-clean" routerClient={client} tasks={fixtureTasks} tasksById={tasksById} />,
    );
    // session-files-clean: five files, one of them scratch.
    await waitFor(() => expect(screen.getByLabelText("Changes, 4 files")).toBeTruthy());
  });

  it("onOpenChanges gets the file plus the rail's tier and turn; the footer link opens the tab", async () => {
    const onOpenChanges = vi.fn();
    render(
      <ContextRail
        sessionId="session-active"
        routerClient={new FakeRouterClient()}
        tasks={fixtureTasks}
        tasksById={tasksById}
        onOpenChanges={onOpenChanges}
      />,
    );
    expand();
    fireEvent.click(screen.getByRole("button", { name: "Changes" }));
    await waitFor(() => expect(screen.getByText("ingest.go")).toBeTruthy());
    fireEvent.click(screen.getByText("ingest.go"));
    expect(onOpenChanges).toHaveBeenLastCalledWith({
      path: "services/data/internal/ingest.go",
      tier: "source",
      turn: null,
    });
    fireEvent.click(screen.getByRole("button", { name: "Open in Changes →" }));
    expect(onOpenChanges).toHaveBeenLastCalledWith({ tier: "source", turn: null });
  });

  it("falls back to onOpenFile for a file click when onOpenChanges isn't given", async () => {
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
    expand();
    fireEvent.click(screen.getByRole("button", { name: "Changes" }));
    await waitFor(() => expect(screen.getByText("ingest.go")).toBeTruthy());
    fireEvent.click(screen.getByText("ingest.go"));
    expect(onOpenFile).toHaveBeenCalledWith("services/data/internal/ingest.go");
  });
});
