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

  it("creates a new agent from the form fields", async () => {
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

  it("renders the Permissions section with the agent's declared intents", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const agent: Agent = {
      id: "a1",
      name: "restricted-agent",
      prompt: "p",
      created_at: "t",
      updated_at: "t",
      permissions: { denied_commands: ["rm"], allowed_paths: ["src/"] },
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

    expect(screen.getByText("Permissions")).toBeTruthy();
    expect(screen.getByText("Block commands")).toBeTruthy();
    expect(screen.getByText("Restrict file paths")).toBeTruthy();
    expect(screen.getByText("Read-only mode")).toBeTruthy();
    expect(screen.getByText("Require approval")).toBeTruthy();
    expect(screen.getByText("rm ×")).toBeTruthy();
    expect(screen.getByText("src/ ×")).toBeTruthy();
    // Never claims enforcement the editor has no provider context to back up.
    expect(screen.queryByText(/enforced by/)).toBeNull();
  });

  it("edits denied_commands and allowed_paths and includes them in the save payload", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onCreate = vi.fn().mockResolvedValue({} as Agent);

    render(
      <AgentEditor
        agent={null}
        onClose={vi.fn()}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "restricted-agent" },
    });
    fireEvent.change(screen.getByText("Prompt").closest("label")!.querySelector("textarea")!, {
      target: { value: "You are careful." },
    });

    const denyInput = screen.getByPlaceholderText("e.g. rm, git push --force");
    fireEvent.change(denyInput, { target: { value: "rm" } });
    fireEvent.click(denyInput.parentElement!.querySelector("button")!);

    const pathInput = screen.getByPlaceholderText("e.g. src/, docs/");
    fireEvent.change(pathInput, { target: { value: "src/" } });
    fireEvent.click(pathInput.parentElement!.querySelector("button")!);

    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      permissions: { denied_commands: ["rm"], allowed_paths: ["src/"] },
    });
  });
});
