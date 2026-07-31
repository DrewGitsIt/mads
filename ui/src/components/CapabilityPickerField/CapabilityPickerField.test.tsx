import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { CapabilityPickerField } from "./CapabilityPickerField";
import type { Capability } from "../../router/types";

function makeCapability(overrides: Partial<Capability>): Capability {
  return {
    id: overrides.id ?? "cap-1",
    name: overrides.name ?? "grep-search",
    type: overrides.type ?? "skill",
    config: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("CapabilityPickerField", () => {
  it("shows an empty-state hint pointing at the Capabilities shelf when the registry has no matching type", () => {
    render(
      <CapabilityPickerField
        label="Skills"
        type="skill"
        capabilities={[makeCapability({ type: "mcp", name: "github" })]}
        value={[]}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByText(/Library → Capabilities/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Add skills/i })).toBeNull();
  });

  it("renders selected names as chips", () => {
    render(
      <CapabilityPickerField
        label="Skills"
        type="skill"
        capabilities={[makeCapability({ name: "grep-search" })]}
        value={["grep-search"]}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByText("grep-search ×")).toBeTruthy();
  });

  it("picking a candidate from the registry calls onChange with its name", () => {
    const onChange = vi.fn();
    render(
      <CapabilityPickerField
        label="Skills"
        type="skill"
        capabilities={[makeCapability({ name: "grep-search" }), makeCapability({ id: "cap-2", name: "web-fetch" })]}
        value={[]}
        onChange={onChange}
      />,
    );

    fireEvent.click(screen.getByText("+ Add skills…"));
    fireEvent.click(screen.getByText("grep-search"));

    expect(onChange).toHaveBeenCalledWith(["grep-search"]);
  });

  it("removing a selected name calls onChange without it", () => {
    const onChange = vi.fn();
    render(
      <CapabilityPickerField
        label="Skills"
        type="skill"
        capabilities={[makeCapability({ name: "grep-search" }), makeCapability({ id: "cap-2", name: "web-fetch" })]}
        value={["grep-search", "web-fetch"]}
        onChange={onChange}
      />,
    );

    fireEvent.click(screen.getByText("grep-search ×"));

    expect(onChange).toHaveBeenCalledWith(["web-fetch"]);
  });

  it("only offers candidates of the matching capability type", () => {
    render(
      <CapabilityPickerField
        label="MCP servers"
        type="mcp"
        capabilities={[makeCapability({ type: "skill", name: "grep-search" }), makeCapability({ id: "cap-2", type: "mcp", name: "github" })]}
        value={[]}
        onChange={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("+ Add mcp servers…"));

    expect(screen.getByText("github")).toBeTruthy();
    expect(screen.queryByText("grep-search")).toBeNull();
  });

  it("offers candidates of every type when type is omitted", () => {
    render(
      <CapabilityPickerField
        label="Capabilities"
        capabilities={[makeCapability({ type: "skill", name: "grep-search" }), makeCapability({ id: "cap-2", type: "mcp", name: "github" })]}
        value={[]}
        onChange={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText("+ Add capabilities…"));

    expect(screen.getByText("grep-search")).toBeTruthy();
    expect(screen.getByText("github")).toBeTruthy();
  });
});
