import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AgentEditor } from "./AgentEditor";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Agent } from "../../router/types";

describe("AgentEditor", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a new persona from the form fields", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onCreate = vi.fn().mockResolvedValue({} as Agent);
    const onClose = vi.fn();

    render(
      <AgentEditor
        agent={null}
        onClose={onClose}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "code-reviewer" },
    });
    fireEvent.change(screen.getByText("Prompt").closest("label")!.querySelector("textarea")!, {
      target: { value: "You review code." },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      name: "code-reviewer",
      prompt: "You review code.",
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("does not show a Delete button for system agents", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const agent: Agent = {
      id: "a1",
      name: "system-agent",
      prompt: "p",
      system: true,
      created_at: "t",
      updated_at: "t",
    };
    render(
      <AgentEditor
        agent={agent}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.queryByText("Delete")).toBeNull();
  });

  it("renders the Constraints section with the persona's allowed_paths and no denied-commands text box", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const agent: Agent = {
      id: "a1",
      name: "restricted-agent",
      prompt: "p",
      created_at: "t",
      updated_at: "t",
      allowlist: ["read", "search"],
      denylist: ["shell"],
      permissions: { allowed_paths: ["src/"] },
    };
    render(
      <AgentEditor
        agent={agent}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    // The boolean surface is present with the shell chip in its denied state.
    expect(screen.getByText("Tool allowances")).toBeTruthy();
    const shellChip = screen.getByTitle(/shell/);
    expect(shellChip).toBeTruthy();
    expect(shellChip.textContent).toContain("deny");

    // Allowed paths still round-trip.
    expect(screen.getByText("Constraints")).toBeTruthy();
    expect(screen.getByText("Restrict file paths")).toBeTruthy();
    expect(screen.getByText("src/ ×")).toBeTruthy();

    // The legacy denied-commands CSV input is gone (replaced by the shell chip).
    expect(screen.queryByPlaceholderText("e.g. rm, git push --force")).toBeNull();

    // Never claims enforcement the editor has no provider context to back up.
    expect(screen.queryByText(/enforced by/)).toBeNull();
  });

  it("keeps a persona's legacy allowed_paths in the save payload when nothing changes", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const agent: Agent = {
      id: "a1",
      name: "restricted-agent",
      prompt: "You are careful.",
      created_at: "t",
      updated_at: "t",
      allowlist: ["read"],
      denylist: ["shell"],
      permissions: { allowed_paths: ["src/"] },
    };
    const onUpdate = vi.fn().mockResolvedValue({} as Agent);
    const onClose = vi.fn();

    render(
      <AgentEditor
        agent={agent}
        onClose={onClose}
        onCreate={vi.fn()}
        onUpdate={onUpdate}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onUpdate).toHaveBeenCalled());
    expect(onUpdate.mock.calls[0][0]).toMatchObject({
      name: "restricted-agent",
      permissions: { allowed_paths: ["src/"] },
      allowlist: ["read"],
      denylist: ["shell"],
    });
    // denied_commands is no longer part of the save payload — the deny is
    // expressed via the `shell` intent chip, not a free-text command string.
    expect((onUpdate.mock.calls[0][0] as Agent).permissions?.denied_commands).toBeUndefined();
  });
});
