import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { PublishDialog } from "./PublishDialog";
import type { Agent, Capability, Template } from "../../router/types";

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "a1",
    name: "diff-reviewer",
    prompt: "Reviews diffs.",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function makeCapability(overrides: Partial<Capability> = {}): Capability {
  return {
    id: "c1",
    name: "jira",
    type: "mcp",
    config: { url: "https://example.test/mcp" },
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function makeTemplate(overrides: Partial<Template> = {}): Template {
  return {
    id: "t1",
    name: "nightly",
    source: "stages: []",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("PublishDialog", () => {
  it("starts with nothing checked", () => {
    render(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[makeCapability()]}
        templates={[makeTemplate()]}
        pending={false}
        error={null}
        onPublish={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect((screen.getByLabelText("diff-reviewer") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("jira") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("nightly") as HTMLInputElement).checked).toBe(false);
  });

  it("disables Publish while nothing is checked, enables once something is", () => {
    render(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[]}
        templates={[]}
        pending={false}
        error={null}
        onPublish={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    const publishButton = screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement;
    expect(publishButton.disabled).toBe(true);

    fireEvent.click(screen.getByLabelText("diff-reviewer"));
    expect(publishButton.disabled).toBe(false);
  });

  it("disables Publish while pending even with a selection", () => {
    render(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[]}
        templates={[]}
        pending={true}
        error={null}
        onPublish={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByLabelText("diff-reviewer"));
    expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it("maps checked rows across all three sections to the right {type,id} list", async () => {
    const onPublish = vi.fn().mockResolvedValue(undefined);
    render(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[makeCapability()]}
        templates={[makeTemplate()]}
        pending={false}
        error={null}
        onPublish={onPublish}
        onClose={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByLabelText("diff-reviewer"));
    fireEvent.click(screen.getByLabelText("jira"));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));

    await vi.waitFor(() => expect(onPublish).toHaveBeenCalledTimes(1));
    const items = onPublish.mock.calls[0][0] as { type: string; id: string }[];
    expect(items).toHaveLength(2);
    expect(items).toContainEqual({ type: "agent", id: "a1" });
    expect(items).toContainEqual({ type: "capability", id: "c1" });
  });

  it("stays open and shows the error inline when publish fails", async () => {
    const onPublish = vi.fn().mockRejectedValue(new Error("boom"));
    const onClose = vi.fn();
    const { rerender } = render(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[]}
        templates={[]}
        pending={false}
        error={null}
        onPublish={onPublish}
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByLabelText("diff-reviewer"));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await vi.waitFor(() => expect(onPublish).toHaveBeenCalledTimes(1));
    expect(onClose).not.toHaveBeenCalled();

    rerender(
      <PublishDialog
        agents={[makeAgent()]}
        capabilities={[]}
        templates={[]}
        pending={false}
        error="boom"
        onPublish={onPublish}
        onClose={onClose}
      />,
    );
    expect(screen.getByText("boom")).toBeTruthy();
  });
});
