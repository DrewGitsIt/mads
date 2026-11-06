import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { FolderPill } from "./FolderPill";
import { AgentPill } from "./AgentPill";
import { ModelPill } from "./ModelPill";
import { TrustPill } from "./TrustPill";

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

  it("keeps the folder choosable when only isolation is locked, with the lock's label in its place", () => {
    const onIsolationChange = vi.fn();
    render(
      <FolderPill
        dir="/repo/mads"
        onPickDir={vi.fn()}
        onPickWorkspace={vi.fn()}
        isolation="worktree"
        onIsolationChange={onIsolationChange}
        isolationLocked={{ reason: lock.reason, label: "shares dns-tracing's worktree" }}
      />,
    );
    const part = screen.getByTestId("folder-isolation");
    expect(part.textContent).toBe("shares dns-tracing's worktree");
    expect(part.getAttribute("title")).toBe(lock.reason);
    fireEvent.click(screen.getByRole("button", { name: /folder mads/ }));
    // The menu opens for the folder, but offers no isolation switch.
    expect(screen.getByLabelText("Folder path")).toBeTruthy();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(onIsolationChange).not.toHaveBeenCalled();
  });
});
