import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { FilesTab } from "./FilesTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";

vi.mock("@monaco-editor/react", () => ({
  DiffEditor: (props: { original: string; modified: string }) => (
    <div data-testid="fake-diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
}));

describe("FilesTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

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

  it("shows the diff for a selected committed file, labeled with its commit", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);

    fireEvent.click(await screen.findByText("services/data/internal/backoff.go"));

    const editor = await screen.findByTestId("fake-diff-editor");
    expect(editor.getAttribute("data-modified")).toBe(
      "func backoff() time.Duration {\n\treturn baseDelay\n}\n",
    );
    expect(screen.getByText("Diff from commit a1b2c3d")).toBeTruthy();
  });

  it("working-change and committed-file selections are mutually exclusive", async () => {
    const client = new FakeRouterClient();
    render(<FilesTab sessionId="session-files-clean" routerClient={client} />);

    const workingRow = (await screen.findByText("services/data/internal/ingest.go")).closest(
      "button",
    );
    fireEvent.click(workingRow!);
    await screen.findByTestId("fake-diff-editor");
    expect(workingRow?.getAttribute("data-selected")).toBe("true");

    const commitRow = screen.getByText("services/data/internal/backoff.go").closest("button");
    fireEvent.click(commitRow!);
    await screen.findByText("Diff from commit a1b2c3d");

    expect(workingRow?.getAttribute("data-selected")).toBe("false");
    expect(commitRow?.getAttribute("data-selected")).toBe("true");
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
