import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { FolderPill } from "./FolderPill";
import { AgentPill } from "./AgentPill";
import { ModelPill } from "./ModelPill";
import { TrustPill } from "./TrustPill";
import { IsolationPill } from "./IsolationPill";

const lock = { reason: "Linked sessions must share one copy of the repo." };

// Every pill, locked, with the value text it would show unlocked.
const lockedPills: [string, ReactElement, string][] = [
  [
    "FolderPill",
    <FolderPill dir="/repo/mads" onPickDir={vi.fn()} onPickWorkspace={vi.fn()} locked={lock} />,
    "mads",
  ],
  ["AgentPill", <AgentPill value="auto" onChange={vi.fn()} onAuto={vi.fn()} locked={lock} />, "Auto"],
  [
    "ModelPill",
    <ModelPill provider="claude-code" model="opus" onChange={vi.fn()} locked={lock} />,
    "opus · claude-code",
  ],
  ["TrustPill", <TrustPill value="full" onChange={vi.fn()} locked={lock} />, "Full"],
  ["IsolationPill", <IsolationPill value="worktree" onChange={vi.fn()} locked={lock} />, "New worktree"],
];

describe("locked pills", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify([]), { status: 200 })),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it.each(lockedPills)("%s shows its value with the reason as tooltip and opens no menu", (_, el, text) => {
    render(el);
    const pill = screen.getByRole("button");
    expect(pill.getAttribute("aria-disabled")).toBe("true");
    expect(pill.getAttribute("title")).toBe(lock.reason);
    expect(pill.textContent).toContain(text);

    fireEvent.click(pill);
    // Still the only control on screen: no popover menu opened.
    expect(screen.getAllByRole("button")).toHaveLength(1);
  });

  it("shows the lock's label in place of the pill's own value", () => {
    const onChange = vi.fn();
    render(
      <IsolationPill
        value="worktree"
        onChange={onChange}
        locked={{ reason: lock.reason, label: "shares dns-tracing's worktree" }}
      />,
    );
    const pill = screen.getByRole("button");
    expect(pill.textContent).toContain("shares dns-tracing's worktree");
    expect(pill.textContent).not.toContain("New worktree");
    fireEvent.click(pill);
    expect(screen.queryByText("Current directory")).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });
});
