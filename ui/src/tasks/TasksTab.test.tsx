import { describe, expect, it } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { TasksTab } from "./TasksTab";
import { FakeRouterClient } from "../router/fixtures";

describe("TasksTab", () => {
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
    await waitFor(() => expect(screen.getByTestId("graph-canvas-svg")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "List" }));
    expect(screen.queryByTestId("graph-canvas-svg")).toBeNull();
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
