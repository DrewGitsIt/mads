import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ConversationHeader } from "../Header/ConversationHeader";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import { resetProviderModelCache } from "../../router/useProviderModels";
import type { Session } from "../../router/types";

function renderHeader(session: Session, work: Partial<{ landing: boolean; committing: boolean }> = {}) {
  const onCommit = vi.fn();
  const onLand = vi.fn();
  render(
    <ConversationHeader
      session={session}
      workspaceName="mads"
      mode="chat"
      onModeChange={vi.fn()}
      work={{ onCommit, onLand, ...work }}
    />,
  );
  return { onCommit, onLand };
}

describe("header Commit / Land control (D4)", () => {
  beforeEach(() => {
    resetProviderModelCache();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("offers Commit… with the file count while there are uncommitted files", () => {
    const { onCommit } = renderHeader({ ...fixtureIsolatedSession, unlanded_count: 0 });
    const button = screen.getByRole("button", { name: /Commit…/ });
    expect(button.textContent).toBe("Commit…4 files");
    fireEvent.click(button);
    expect(onCommit).toHaveBeenCalled();
  });

  it("notes 'then land N' when there are commits waiting too", () => {
    renderHeader(fixtureIsolatedSession);
    expect(screen.getByRole("button", { name: /Commit…/ }).textContent).toBe("Commit…4 filesthen land 3");
    expect(screen.queryByRole("button", { name: /Land into/ })).toBeNull();
  });

  it("offers Land into <base> with the commit count once the folder is clean", () => {
    const { onLand } = renderHeader({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 1 });
    const button = screen.getByRole("button", { name: /Land into main/ });
    expect(button.textContent).toBe("Land into main1 commit");
    fireEvent.click(button);
    expect(onLand).toHaveBeenCalled();
  });

  it("shows Landing… while a Land is in flight", () => {
    renderHeader({ ...fixtureIsolatedSession, uncommitted_count: 0 }, { landing: true });
    const button = screen.getByRole("button", { name: "Landing…" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  it("is hidden when there's nothing to commit or land", () => {
    renderHeader({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 });
    expect(document.querySelector("[data-work-state]")).toBeNull();
  });

  it("is hidden for a session that isn't in git", () => {
    renderHeader(fixtureSessions["session-active"]);
    expect(document.querySelector("[data-work-state]")).toBeNull();
  });

  it("never offers Land for a session working in place, only Commit", () => {
    const inPlace: Session = {
      ...fixtureIsolatedSession,
      worktree_dir: undefined,
      worktree_branch: undefined,
      worktree_base: undefined,
      uncommitted_count: 1,
      unlanded_count: 3,
    };
    renderHeader(inPlace);
    expect(screen.getByRole("button", { name: /Commit…/ }).textContent).toBe("Commit…1 file");
    renderHeader({ ...inPlace, uncommitted_count: 0 });
    expect(screen.queryByRole("button", { name: /Land into/ })).toBeNull();
  });

  it("holds Land back while files off the change list are changed, asking the agent to tidy up instead", () => {
    const onAskAgent = vi.fn();
    const onLand = vi.fn();
    render(
      <ConversationHeader
        session={{ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 3, outside_count: 5 }}
        work={{ onCommit: vi.fn(), onLand, onAskAgent }}
      />,
    );
    expect(screen.queryByRole("button", { name: /Land into/ })).toBeNull();
    const button = screen.getByRole("button", { name: /Changes outside this session/ });
    expect(button.textContent).toBe("Changes outside this session5 files");
    expect(button.getAttribute("title")).toMatch(/^5 files in this session's folder changed outside its change list/);
    fireEvent.click(button);
    expect(onLand).not.toHaveBeenCalled();
    expect(onAskAgent).toHaveBeenCalledWith(expect.stringContaining("Commit the ones that belong to this work"));
  });

  it("still offers Commit… first when the session's own files are uncommitted too", () => {
    renderHeader({ ...fixtureIsolatedSession, outside_count: 2 });
    expect(screen.getByRole("button", { name: /Commit…/ })).toBeTruthy();
    expect(document.querySelector('[data-work-state="outside"]')).toBeNull();
  });

  it("ignores the outside count for a session working in place", () => {
    renderHeader({
      ...fixtureIsolatedSession,
      worktree_dir: undefined,
      worktree_branch: undefined,
      worktree_base: undefined,
      uncommitted_count: 0,
      outside_count: 2,
    });
    expect(document.querySelector("[data-work-state]")).toBeNull();
  });

  it("subtitles an isolated session with workspace / branch → base", () => {
    renderHeader(fixtureIsolatedSession);
    expect(screen.getByTestId("conversation-subline").textContent).toBe("mads / mads/session/session-isolated → main");
  });

  it("re-renders from a live record update", () => {
    const { rerender } = render(
      <ConversationHeader session={{ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 }} work={{ onCommit: vi.fn(), onLand: vi.fn() }} />,
    );
    expect(document.querySelector("[data-work-state]")).toBeNull();
    rerender(
      <ConversationHeader session={{ ...fixtureIsolatedSession, uncommitted_count: 2 }} work={{ onCommit: vi.fn(), onLand: vi.fn() }} />,
    );
    expect(screen.getByRole("button", { name: /Commit…/ }).textContent).toContain("2 files");
  });
});
