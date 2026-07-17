import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { FilesTab } from "./FilesTab";
import { FakeRouterClient } from "../router/fixtures";

vi.mock("@monaco-editor/react", () => ({
  DiffEditor: (props: { original: string; modified: string }) => (
    <div data-testid="fake-diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
}));

describe("FilesTab", () => {
  it("renders the session's file changes and commits", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);

    expect(await screen.findByText("services/data/internal/ingest.go")).toBeTruthy();
    expect(screen.getByText("services/data/internal/ingest_test.go")).toBeTruthy();
    expect(await screen.findByText(/ingest: add retry backoff/)).toBeTruthy();
  });

  it("shows the diff for a selected file", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);

    fireEvent.click(await screen.findByText("services/data/internal/ingest.go"));

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-original")).toBe("func ingest() {\n\ttime.Sleep(retryDelay)\n}\n");
  });

  it("renders the merge-back flow's status chip", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);
    expect(await screen.findByText("Not merged")).toBeTruthy();
  });

  it("hides the scope switch when no taskId is given", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);
    await screen.findByText("services/data/internal/ingest.go");
    expect(screen.queryByText("Whole session")).toBeNull();
  });

  it("scopes the commit list to the task once This task is selected", async () => {
    const client = new FakeRouterClient();
    render(
      <FilesTab sessionId="session-files-observed" taskId="tsk_flaky-metrics" routerClient={client} />,
    );

    expect(await screen.findByText(/chore: bump go.sum/)).toBeTruthy();

    fireEvent.click(screen.getByText("This task"));

    expect(await screen.findByText(/router: filter control events/)).toBeTruthy();
    expect(screen.getByText(/router: candidate fix/)).toBeTruthy();
    expect(screen.queryByText(/chore: bump go.sum/)).toBeNull();
  });

  it("shows a single honest empty state when there are no commits and no working changes", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-empty" routerClient={client} />);

    expect(
      await screen.findByText(/no commits recorded and no working changes observed/i),
    ).toBeTruthy();
    expect(screen.queryByText("Committed")).toBeNull();
    expect(screen.queryByText("Working changes")).toBeNull();
  });

  it("renders only the Committed section when only commits are non-empty", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-commits-only" routerClient={client} />);

    expect(await screen.findByText("Committed")).toBeTruthy();
    expect(screen.queryByText("Working changes")).toBeNull();
  });

  it("renders commits newest-first", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-observed" routerClient={client} />);

    const messages = await screen.findAllByText(
      /router: filter control events|router: candidate fix|chore: bump go\.sum/,
    );
    expect(messages.map((el) => el.textContent)).toEqual([
      "chore: bump go.sum",
      "router: candidate fix for overlapping file touches",
      "router: filter control events by session_id",
    ]);
  });
});
