import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ToolDiff, ToolUse } from "../../router/types";
import { ActivityDock, type DockView } from "./ActivityDock";

function toolView(tool: ToolUse): DockView {
  return { type: "tool", tool };
}

const noDiffClient = { getToolDiff: vi.fn(async () => null) };

describe("ActivityDock — tool detail", () => {
  const bashTool: ToolUse = {
    id: "t1",
    name: "Bash",
    kind: "execute",
    title: "Run session runner test suite",
    input: { command: "go test ./services/session/..." },
    result: { text: "ok  github.com/drewkidwell/mads/services/session 0.41s" },
  };

  it("renders title, input and result", () => {
    render(
      <ActivityDock view={toolView(bashTool)} sessionId="s1" client={noDiffClient} onClose={() => {}} />,
    );
    expect(screen.getByText("Run session runner test suite")).toBeTruthy();
    expect(screen.getByText(/go test .\/services\/session/)).toBeTruthy();
    expect(screen.getByText(/ok github\.com\/drewkidwell/)).toBeTruthy();
  });

  it("labels a blocked call's result 'blocked', with the blocked glyph", () => {
    const refused: ToolUse = { ...bashTool, result: { text: "denied by user", is_error: true } };
    render(
      <ActivityDock
        view={toolView(refused)}
        sessionId="s1"
        client={noDiffClient}
        statuses={new Map([["t1", "blocked" as const]])}
        onClose={() => {}}
      />,
    );
    expect(screen.getByText("blocked")).toBeTruthy();
    expect(screen.queryByText("error")).toBeNull();
    expect(screen.getByRole("img", { name: "blocked by permissions" })).toBeTruthy();
  });

  it("renders a bash call's bare command — no JSON braces, no description echo (it's already the title)", () => {
    const tool: ToolUse = {
      ...bashTool,
      input: {
        command: "make check",
        description: "Run session runner test suite",
        timeout: 600000,
      },
    };
    render(
      <ActivityDock view={toolView(tool)} sessionId="s1" client={noDiffClient} onClose={() => {}} />,
    );
    expect(screen.getByText("Bash · command")).toBeTruthy();
    expect(screen.getByText("make check")).toBeTruthy();
    // remaining option keys survive under their own label…
    expect(screen.getByText("options")).toBeTruthy();
    expect(screen.getByText(/"timeout": 600000/)).toBeTruthy();
    // …but the raw JSON shape (command/description keys) is gone
    expect(screen.queryByText(/"command"/)).toBeNull();
    expect(screen.queryByText(/"description"/)).toBeNull();
  });

  it("closes on Escape and on outside click, but not on inside clicks", () => {
    const onClose = vi.fn();
    render(
      <div>
        <button type="button" data-testid="outside">
          outside
        </button>
        <ActivityDock view={toolView(bashTool)} sessionId="s1" client={noDiffClient} onClose={onClose} />
      </div>,
    );
    fireEvent.mouseDown(screen.getByText("Run session runner test suite"));
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.mouseDown(screen.getByTestId("outside"));
    expect(onClose).toHaveBeenCalledTimes(1);

    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("retargets when the view changes to another row's tool", () => {
    const { rerender } = render(
      <ActivityDock view={toolView(bashTool)} sessionId="s1" client={noDiffClient} onClose={() => {}} />,
    );
    expect(screen.getByText("Run session runner test suite")).toBeTruthy();
    rerender(
      <ActivityDock
        view={toolView({ id: "t2", name: "Read", title: "runner.go", input: { file_path: "runner.go" } })}
        sessionId="s1"
        client={noDiffClient}
        onClose={() => {}}
      />,
    );
    expect(screen.queryByText("Run session runner test suite")).toBeNull();
    expect(screen.getByText("runner.go")).toBeTruthy();
  });

  it("says so honestly when an interrupted call has no result", () => {
    render(
      <ActivityDock
        view={toolView({ id: "t3", name: "Bash", status: "interrupted", input: { command: "go test ./ui/..." } })}
        sessionId="s1"
        client={noDiffClient}
        onClose={() => {}}
      />,
    );
    expect(screen.getByText(/turn interrupted before the result arrived/)).toBeTruthy();
  });
});

describe("ActivityDock — historical diff (T13/T10)", () => {
  const editTool: ToolUse = {
    id: "t1",
    name: "Edit",
    kind: "edit",
    title: "runner.go · classify first touch",
    path: "runner.go",
    input: { old_string: "verdict := old", new_string: "verdict := stat" },
  };

  it("prefers the shadow store's recorded diff when the route answers 200", async () => {
    const client = {
      getToolDiff: vi.fn(
        async (): Promise<ToolDiff> => ({
          path: "services/session/internal/runner/runner.go",
          original: "shared\nverdict := recorded-old\ntail",
          modified: "shared\nverdict := recorded-new\ntail",
        }),
      ),
    };
    render(<ActivityDock view={toolView(editTool)} sessionId="s1" client={client} onClose={() => {}} />);

    await waitFor(() => expect(screen.getByText(/recorded at execution time/)).toBeTruthy());
    expect(client.getToolDiff).toHaveBeenCalledWith("s1", "t1");
    // The trimmed changed region from the server content, not the input.
    expect(screen.getByText("verdict := recorded-old")).toBeTruthy();
    expect(screen.getByText("verdict := recorded-new")).toBeTruthy();
    expect(screen.queryByText("verdict := old")).toBeNull();
  });

  it("keeps the input-derived diff on 404 (null) — the documented degrade", async () => {
    render(<ActivityDock view={toolView(editTool)} sessionId="s1" client={noDiffClient} onClose={() => {}} />);
    await waitFor(() => expect(noDiffClient.getToolDiff).toHaveBeenCalled());
    expect(screen.getByText("verdict := old")).toBeTruthy();
    expect(screen.getByText("verdict := stat")).toBeTruthy();
  });

  it("keeps the input-derived diff when the fetch throws", async () => {
    const client = { getToolDiff: vi.fn(async () => Promise.reject(new Error("network"))) };
    render(<ActivityDock view={toolView(editTool)} sessionId="s1" client={client} onClose={() => {}} />);
    await waitFor(() => expect(client.getToolDiff).toHaveBeenCalled());
    expect(screen.getByText("verdict := old")).toBeTruthy();
  });

  it("renders the too-large marker honestly", async () => {
    const client = {
      getToolDiff: vi.fn(
        async (): Promise<ToolDiff> => ({
          path: "big.bin",
          original: "",
          modified: "tiny",
          original_too_large: true,
        }),
      ),
    };
    render(<ActivityDock view={toolView(editTool)} sessionId="s1" client={client} onClose={() => {}} />);
    await waitFor(() => expect(screen.getByTestId("diff-too-large")).toBeTruthy());
    expect(screen.getByText(/before-image exceeded the capture limit/)).toBeTruthy();
  });
});

describe("ActivityDock — subagent stream (T15)", () => {
  const view: DockView = {
    type: "subagent",
    parentToolUseId: "tu1",
    title: "Verify classification end-to-end",
    subtype: "general-purpose",
    items: [
      { type: "text", text: "Spawning a session against the fake provider." },
      {
        type: "tool",
        tool: { id: "s1", name: "Bash", kind: "execute", title: "Start disposable session", result: { text: "ok" } },
      },
      { type: "tool", tool: { id: "s2", name: "Bash", kind: "execute", title: "Drive create/edit/delete" } },
    ],
    task: {
      taskId: "tk1",
      toolUseId: "tu1",
      description: "Verify classification end-to-end",
      subagentType: "general-purpose",
      totalTokens: 9800,
      toolUses: 4,
      running: true,
      completed: false,
    },
  };

  it("renders the subagent's own stream with the same grouping components", () => {
    render(<ActivityDock view={view} sessionId="s1" client={noDiffClient} onClose={() => {}} />);
    expect(screen.getByText("Spawning a session against the fake provider.")).toBeTruthy();
    // Grouped, open, with rows — the same ActivityGroup vocabulary.
    expect(screen.getByTestId("activity-group")).toBeTruthy();
    expect(screen.getByText("Start disposable session")).toBeTruthy();
    expect(screen.getByText("Drive create/edit/delete")).toBeTruthy();
    expect(screen.getByText("9.8k tok · 4 tools")).toBeTruthy();
  });

  it("shows a subagent call's blocked status and its permission card under its row", () => {
    render(
      <ActivityDock
        view={view}
        sessionId="s1"
        client={noDiffClient}
        statuses={new Map([["s1", "blocked" as const]])}
        permissions={
          new Map([
            ["s2", [{ id: "perm-2", tool: "Bash", status: "pending", tool_use_id: "s2", parent_tool_use_id: "tu1" }]],
          ])
        }
        onClose={() => {}}
      />,
    );
    const [first, second] = screen.getAllByTestId("activity-row");
    expect(first.querySelector('[data-status="blocked"]')).toBeTruthy();
    expect(second.nextElementSibling!.textContent).toContain("awaiting your decision");
  });
});
