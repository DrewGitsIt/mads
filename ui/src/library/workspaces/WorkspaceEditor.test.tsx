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
    fireEvent.change(screen.getByPlaceholderText("e.g. ./services/router"), {
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

    await waitFor(() => expect(screen.getByText("deploy")).toBeTruthy());
    fireEvent.change(screen.getByText("Default template").closest("label")!.querySelector("select")!, {
      target: { value: "deploy" },
    });
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "mads" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({ defaults: { template: "deploy" } });
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
