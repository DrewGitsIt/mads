import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { WORKSPACE_TABS } from "./tabRegistry";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakePipelineClient } from "../../../pipeline/client/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";

const SESSION_ID = "session-active";

describe("WORKSPACE_TABS", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("has exactly 4 entries in the expected order with the expected labels", () => {
    expect(WORKSPACE_TABS.map((t) => ({ id: t.id, label: t.label }))).toEqual([
      { id: "conversation", label: "Conversation" },
      { id: "tasks", label: "Tasks" },
      { id: "files", label: "Files" },
      { id: "pipeline", label: "Pipeline" },
    ]);
  });

  it("ConversationTabView renders the real ConversationTab", async () => {
    const routerClient = new FakeRouterClient();
    const def = WORKSPACE_TABS.find((t) => t.id === "conversation")!;
    render(
      <def.component
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
      />,
    );
    await waitFor(() => expect(screen.getByText("Chat")).toBeTruthy());
    expect(screen.getByText("Terminal")).toBeTruthy();
  });

  it("ConversationTabView also mounts the Context Rail, identical regardless of chat/terminal mode", async () => {
    const routerClient = new FakeRouterClient();
    const def = WORKSPACE_TABS.find((t) => t.id === "conversation")!;
    render(
      <def.component
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
      />,
    );
    // Rail chrome (tab switcher) renders regardless of display mode.
    await waitFor(() => expect(screen.getByText("Changes")).toBeTruthy());
    // A fixture synthesis proposal card renders inside the rail.
    await waitFor(() =>
      expect(screen.getByText("Add a metrics counter for retry exhaustion")).toBeTruthy(),
    );
  });

  it("TasksTabView renders the real TasksTab", async () => {
    const routerClient = new FakeRouterClient();
    const def = WORKSPACE_TABS.find((t) => t.id === "tasks")!;
    render(
      <def.component
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
      />,
    );
    await waitFor(() => expect(screen.getByText("List")).toBeTruthy());
    expect(screen.getByText("Graph")).toBeTruthy();
  });

  it("FilesTabView renders the real FilesTab", async () => {
    const routerClient = new FakeRouterClient();
    const def = WORKSPACE_TABS.find((t) => t.id === "files")!;
    render(
      <def.component
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
      />,
    );
    await waitFor(() => expect(screen.getByText("Working changes")).toBeTruthy());
  });

  it("PipelineTabView renders the real PipelineTab", async () => {
    const routerClient = new FakeRouterClient();
    const def = WORKSPACE_TABS.find((t) => t.id === "pipeline")!;
    render(
      <def.component
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
      />,
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
  });
});
