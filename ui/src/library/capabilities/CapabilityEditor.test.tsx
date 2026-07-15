import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CapabilityEditor } from "./CapabilityEditor";
import type { Capability } from "../../router/types";

describe("CapabilityEditor", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a skill capability with a path", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    const onCreate = vi.fn().mockResolvedValue({} as Capability);
    const onClose = vi.fn();

    render(
      <CapabilityEditor
        capability={null}
        onClose={onClose}
        onCreate={onCreate}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Name").closest("label")!.querySelector("input")!, {
      target: { value: "test-runner" },
    });
    fireEvent.change(screen.getByText("Path").closest("label")!.querySelector("input")!, {
      target: { value: "./skills/test-runner" },
    });
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(onCreate).toHaveBeenCalled());
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      name: "test-runner",
      type: "skill",
      config: { path: "./skills/test-runner" },
    });
  });

  it("switches to command/url fields for the mcp type", () => {
    render(
      <CapabilityEditor
        capability={null}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByText("Type").closest("label")!.querySelector("select")!, {
      target: { value: "mcp" },
    });

    expect(screen.getByText("Command")).toBeTruthy();
    expect(screen.getByText("URL")).toBeTruthy();
    expect(screen.queryByText("Path")).toBeNull();
  });

  it("renders the Diagnose panel and existing capability's credential env vars when editing", () => {
    const capability: Capability = {
      id: "c1",
      name: "github",
      type: "mcp",
      config: { command: "gh-mcp" },
      credential_env_vars: ["GITHUB_TOKEN"],
      created_at: "t",
      updated_at: "t",
    };
    render(
      <CapabilityEditor
        capability={capability}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText("Diagnose")).toBeTruthy();
    expect(screen.getByText("GITHUB_TOKEN ×")).toBeTruthy();
  });
});
