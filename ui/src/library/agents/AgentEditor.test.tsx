import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AgentEditor, partitionRegistered } from "./AgentEditor";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Agent, Capability } from "../../router/types";

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
  // Routes the editor's two fetches: the capability registry and the
  // suggest-capabilities enrichment call.
  function stubRegistryAndSuggestion(registry: Partial<Capability>[], suggestion: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: URL | string) => {
        const path = url.toString();
        if (path.includes("/agents/suggest-capabilities")) {
          return Promise.resolve(new Response(JSON.stringify(suggestion), { status: 200 }));
        }
        if (path.includes("/capabilities")) {
          return Promise.resolve(new Response(JSON.stringify(registry), { status: 200 }));
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
  }

  it("applies only registered suggestions and names the skipped ones", async () => {
    stubRegistryAndSuggestion(
      [{ id: "c1", name: "postgres", type: "mcp" }],
      { mcp_servers: ["postgres", "jira"], skills: ["migrations"], dropped: ["made-up"] },
    );
    const onCreate = vi.fn().mockResolvedValue({} as Agent);
    render(
      <AgentEditor agent={null} onClose={vi.fn()} onCreate={onCreate} onUpdate={vi.fn()} onDelete={vi.fn()} />,
    );
    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "db-migrator" },
    });
    fireEvent.change(screen.getByText("Prompt").closest("label")!.querySelector("textarea")!, {
      target: { value: "You run migrations." },
    });
    // Let the registry load before suggesting, so the client-side check runs.
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.length).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 0));

    fireEvent.click(screen.getByText("Suggest capabilities"));
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toContain("made-up, migrations, jira"),
    );

    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0].capabilities).toEqual({
      tools: undefined,
      mcp_servers: ["postgres"],
      skills: undefined,
    });
  });

  it("blocks a save that references an unregistered capability", async () => {
    stubRegistryAndSuggestion([], {});
    const onUpdate = vi.fn();
    const agent: Agent = {
      id: "a1",
      name: "stale",
      prompt: "p",
      created_at: "t",
      updated_at: "t",
      capabilities: { skills: ["deleted-skill"] },
    };
    render(
      <AgentEditor agent={agent} onClose={vi.fn()} onCreate={vi.fn()} onUpdate={onUpdate} onDelete={vi.fn()} />,
    );
    await new Promise((r) => setTimeout(r, 0));
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(screen.getByText(/Not in the capability registry: deleted-skill/)).toBeTruthy());
    expect(onUpdate).not.toHaveBeenCalled();
  });
});

describe("partitionRegistered", () => {
  it("splits names by whether the registry holds them under that type", () => {
    const registry = [
      { id: "1", name: "pg", type: "mcp" },
      { id: "2", name: "lint", type: "skill" },
    ] as Capability[];
    expect(partitionRegistered(["pg", "lint", "nope"], "mcp", registry)).toEqual({
      kept: ["pg"],
      missing: ["lint", "nope"],
    });
  });
});
