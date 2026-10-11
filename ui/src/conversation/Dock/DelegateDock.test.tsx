import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { DelegateDock } from "./DelegateDock";
import { fixtureSessions } from "../../router/fixtures";
import { contextWindowForModel } from "../../router/modelDisplay";
import type { Delegate, Session } from "../../router/types";
import { resetProviderModelCache } from "../../router/useProviderModels";

const record: Delegate = {
  id: "dlg_1",
  session_id: "session-active",
  harness_id: "a1",
  type: "Explore",
  state: "running",
  runs: 1,
  started_at: "2026-10-08T10:00:00Z",
  created_at: "2026-10-08T10:00:00Z",
  updated_at: "2026-10-08T10:00:00Z",
  model: "claude-haiku-4-5",
  branch: "feature-x",
  usage: { input_tokens: 2, output_tokens: 9, model: "claude-haiku-4-5", context_tokens: 50_000, context_window: 200_000 },
};

const parent: Session = fixtureSessions["session-active"];

function meter(): number {
  return Number(screen.getByRole("meter", { name: "Context" }).getAttribute("aria-valuenow"));
}

describe("DelegateDock", () => {
  beforeEach(() => {
    resetProviderModelCache();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const body = input.toString().endsWith("/providers")
          ? [{ name: "claudecode", matrix: {} }]
          : { models: [{ id: "claude-haiku-4-5", name: "Haiku 4.5" }] };
        return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
      }),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the delegate's own context, type, model and branch — read-only, no trust", async () => {
    render(<DelegateDock delegate={record} parent={parent} workspaceName="mads" />);

    expect(meter()).toBe(25);
    const status = screen.getByTestId("dock-status");
    expect(status.querySelector('[data-segment="persona"]')?.textContent).toContain("Explore");
    await waitFor(() => expect(status.querySelector('[data-segment="model"]')?.textContent).toContain("Haiku 4.5"));
    // Its own branch wins over the parent's.
    expect(screen.getByTestId("dock-location").getAttribute("aria-label")).toBe("mads / feature-x");
    expect(status.querySelector('[data-segment="trust"]')).toBeNull();
    expect(screen.getByTestId("delegate-dock").querySelector("button, input, textarea")).toBeNull();
  });

  it("borrows the parent's reported window for the same model, else the static table's", () => {
    const usage = { input_tokens: 0, output_tokens: 0, context_tokens: 300_000 };
    const scaled: Session = {
      ...parent,
      model: "claude-opus-5[1m]",
      last_usage: { input_tokens: 0, output_tokens: 0, model: "claude-opus-5[1m]", context_window: 1_000_000 },
    };
    const { rerender } = render(
      <DelegateDock delegate={{ ...record, model: "claude-opus-5", usage }} parent={scaled} />,
    );
    expect(meter()).toBe(30);

    rerender(<DelegateDock delegate={{ ...record, model: "claude-haiku-4-5", usage }} parent={scaled} />);
    expect(meter()).toBe(Math.min(100, Math.round((300_000 / contextWindowForModel("claude-haiku-4-5")) * 100)));
  });

  it("falls back to the parent's branch, or names its own worktree", () => {
    const { rerender } = render(
      <DelegateDock delegate={{ ...record, branch: undefined, usage: undefined }} parent={parent} workspaceName="mads" />,
    );
    expect(screen.getByTestId("dock-location").getAttribute("aria-label")).toBe("mads / mads/tsk_retry-backoff");
    // No usage yet: a plain hairline, never a made-up number.
    expect(screen.queryByRole("meter")).toBeNull();

    rerender(
      <DelegateDock
        delegate={{ ...record, branch: undefined, worktree_path: "/repo/.claude/worktrees/agent-a1" }}
        parent={parent}
        workspaceName="mads"
      />,
    );
    const loc = screen.getByTestId("dock-location");
    expect(loc.getAttribute("aria-label")).toBe("mads / agent-a1");
    expect(loc.getAttribute("title")).toBe("Worktree: /repo/.claude/worktrees/agent-a1");
  });
});
