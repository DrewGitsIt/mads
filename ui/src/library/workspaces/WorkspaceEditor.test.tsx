import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { WorkspaceEditor } from "./WorkspaceEditor";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Workspace } from "../../router/types";

describe("WorkspaceEditor", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a new workspace with an added repo, marked primary", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onCreate = vi.fn().mockResolvedValue({} as Workspace);
    const onClose = vi.fn();

    render(
      <WorkspaceEditor
        workspace={null}
        onClose={onClose}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "mads" },
    });
    fireEvent.change(screen.getByLabelText("Repo path"), {
      target: { value: "./services/router" },
    });
    fireEvent.click(screen.getByText("Add repo"));
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      name: "mads",
      repos: [{ path: "./services/router", primary: true }],
    });
  });

  it("stores the selected template's name into defaults.template", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([{ id: "t1", name: "deploy", source: "x", created_at: "t", updated_at: "t" }]),
          { status: 200 },
        ),
      ),
    );
    const onCreate = vi.fn().mockResolvedValue({} as Workspace);

    render(
      <WorkspaceEditor
        workspace={null}
        onClose={vi.fn()}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    // Open the themed template Select and pick "deploy" (options only render while open).
    fireEvent.click(screen.getByLabelText("Default template"));
    fireEvent.click(await screen.findByText("deploy"));
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "mads" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({ defaults: { template: "deploy" } });
  });

  it("populates the agent and model dropdowns from the registry and saves the picks", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url.includes("/agents")) {
          return Promise.resolve(
            new Response(JSON.stringify([{ id: "a1", name: "reviewer", prompt: "p" }]), {
              status: 200,
            }),
          );
        }
        if (url.endsWith("/providers")) {
          return Promise.resolve(
            new Response(JSON.stringify([{ name: "anthropic", matrix: {} }]), { status: 200 }),
          );
        }
        if (url.includes("/providers/anthropic/models")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({ models: [{ id: "claude-opus-5", name: "Claude Opus 5" }] }),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    const onCreate = vi.fn().mockResolvedValue({} as Workspace);

    render(
      <WorkspaceEditor
        workspace={null}
        onClose={vi.fn()}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByLabelText("Default agent"));
    fireEvent.click(await screen.findByText("reviewer"));
    fireEvent.click(screen.getByLabelText("Default model"));
    fireEvent.click(await screen.findByText("Claude Opus 5"));
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "mads" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      defaults: { agent: "reviewer", model: "claude-opus-5" },
    });
  });

  it("edits capabilities and includes them in the save payload", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url.includes("/capabilities")) {
          return Promise.resolve(
            new Response(
              JSON.stringify([
                {
                  id: "cap-verify",
                  name: "verify",
                  type: "skill",
                  config: {},
                  created_at: "t",
                  updated_at: "t",
                },
              ]),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    const onCreate = vi.fn().mockResolvedValue({} as Workspace);

    render(
      <WorkspaceEditor
        workspace={null}
        onClose={vi.fn()}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    await waitFor(() => expect(screen.getByText("+ Add capabilities…")).toBeTruthy());
    fireEvent.click(screen.getByText("+ Add capabilities…"));
    fireEvent.click(screen.getByText("verify"));
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "mads" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({ capabilities: ["verify"] });
  });

  it("shows UsedBySessions only when editing an existing workspace", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(
      <WorkspaceEditor
        workspace={null}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.queryByText("Used by")).toBeNull();
  });
});
