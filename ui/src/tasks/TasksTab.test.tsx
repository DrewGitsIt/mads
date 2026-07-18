import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { TasksTab } from "./TasksTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";

describe("TasksTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads tasks and renders the list view by default", async () => {
    const client = new FakeRouterClient();
    render(<TasksTab sessionId="session-active" routerClient={client} />);

    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );
  });

  it("toggles to the graph view and back", async () => {
    const client = new FakeRouterClient();
    render(<TasksTab sessionId="session-active" routerClient={client} />);
    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Graph" }));
    await waitFor(() => expect(screen.getByTestId("rf__wrapper")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "List" }));
    expect(screen.queryByTestId("rf__wrapper")).toBeNull();
  });

  it("opens the detail sheet on selecting a task, editable and closable", async () => {
    const client = new FakeRouterClient();
    render(<TasksTab sessionId="session-active" routerClient={client} />);
    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );

    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
    const titleInput = (await screen.findByLabelText("Title")) as HTMLInputElement;
    expect(titleInput.value).toBe("Add retry backoff to the ingest worker");

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByLabelText("Title")).toBeNull();
  });

  it("opens bulk import and the detail sheet mutually exclusively", async () => {
    const client = new FakeRouterClient();
    render(<TasksTab sessionId="session-active" routerClient={client} />);
    await waitFor(() =>
      expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Bulk import" }));
    expect(await screen.findByText("Bulk import tasks")).toBeTruthy();

    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
    expect(await screen.findByLabelText("Title")).toBeTruthy();
    expect(screen.queryByText("Bulk import tasks")).toBeNull();
  });
});
