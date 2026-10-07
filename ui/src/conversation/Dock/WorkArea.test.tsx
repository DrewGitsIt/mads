import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { SessionDock, type DockWork } from "./SessionDock";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import { resetProviderModelCache } from "../../router/useProviderModels";
import { resetProviderInfoCache } from "../../router/useProviderInfos";
import type { Session } from "../../router/types";
import type { LandCardModel } from "../Worktree/worktreeCards";

function dock(session: Session, work: Partial<DockWork> = {}, landOutcome?: LandCardModel) {
  return (
    <SessionDock
      session={session}
      workspaceName="mads"
      mode="chat"
      onModeChange={vi.fn()}
      work={{ onCommit: work.onCommit ?? vi.fn(), onLand: work.onLand ?? vi.fn(), ...work }}
      landOutcome={landOutcome}
    />
  );
}

function renderDock(session: Session, work: Partial<DockWork> = {}, landOutcome?: LandCardModel) {
  const onCommit = work.onCommit ?? vi.fn();
  const onLand = work.onLand ?? vi.fn();
  const utils = render(dock(session, { ...work, onCommit, onLand }, landOutcome));
  return { ...utils, onCommit, onLand };
}

const control = () => document.querySelector("[data-work-state]") as HTMLButtonElement | null;
const clean = { ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 1 };

const refusedOutside: LandCardModel = {
  kind: "refused",
  id: "wt-9",
  seq: 9,
  base: "main",
  reason: "dirty",
  files: ["ui/e2e/sessions.spec.ts", "setup_dock.sh"],
  outside: ["ui/e2e/sessions.spec.ts", "setup_dock.sh"],
  latest: true,
};

describe("dock Commit / Land control (D4)", () => {
  beforeEach(() => {
    resetProviderModelCache();
    resetProviderInfoCache();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("offers Commit… with the file count while there are uncommitted files", () => {
    const { onCommit } = renderDock({ ...fixtureIsolatedSession, unlanded_count: 0 });
    const button = control()!;
    expect(button.getAttribute("data-work-state")).toBe("commit");
    expect(button.textContent).toBe("Commit… 4");
    expect(button.getAttribute("title")).toBe("Commit 4 files");
    fireEvent.click(button);
    expect(onCommit).toHaveBeenCalled();
  });

  it("notes 'then land N' when there are commits waiting too", () => {
    renderDock(fixtureIsolatedSession);
    expect(control()!.getAttribute("title")).toBe("Commit 4 files, then land 3 commits");
    expect(screen.queryByRole("button", { name: /Land into/ })).toBeNull();
  });

  it("offers Land into <base> with the commit count once the folder is clean", () => {
    const { onLand } = renderDock(clean);
    const button = screen.getByRole("button", { name: /Land into main/ });
    expect(button.getAttribute("data-work-state")).toBe("land");
    expect(button.textContent).toBe("Land into main ↑1");
    fireEvent.click(button);
    expect(onLand).toHaveBeenCalledTimes(1);
  });

  it("shows Landing… while a Land is in flight", () => {
    renderDock({ ...fixtureIsolatedSession, uncommitted_count: 0 }, { landing: true });
    const button = screen.getByRole("button", { name: "Landing…" }) as HTMLButtonElement;
    expect(button.getAttribute("data-work-state")).toBe("landing");
    expect(button.disabled).toBe(true);
  });

  it("shows Committing… while a one-click Commit is in flight", () => {
    renderDock(fixtureIsolatedSession, { committing: true });
    expect(control()!.disabled).toBe(true);
    expect(control()!.textContent).toContain("Committing…");
  });

  it("is hidden when there's nothing to commit or land", () => {
    renderDock({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 });
    expect(control()).toBeNull();
  });

  it("is hidden for a session that isn't in git", () => {
    renderDock(fixtureSessions["session-active"]);
    expect(control()).toBeNull();
  });

  it("is hidden without work handlers", () => {
    render(<SessionDock session={fixtureIsolatedSession} mode="chat" onModeChange={vi.fn()} />);
    expect(control()).toBeNull();
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
    const { unmount } = renderDock(inPlace);
    expect(control()!.getAttribute("data-work-state")).toBe("commit");
    unmount();
    renderDock({ ...inPlace, uncommitted_count: 0 });
    expect(control()).toBeNull();
  });

  it("holds Land back while files off the change list are changed, explaining in the popover", () => {
    const onAskAgent = vi.fn();
    const { onLand } = renderDock({ ...clean, unlanded_count: 3, outside_count: 5 }, { onAskAgent });
    expect(screen.queryByRole("button", { name: /Land into/ })).toBeNull();
    const button = control()!;
    expect(button.getAttribute("data-work-state")).toBe("outside");
    expect(button.getAttribute("aria-label")).toBe("Changes outside this session · 5 files");
    expect(button.getAttribute("title")).toMatch(/^5 files in this session's folder changed outside its change list/);

    fireEvent.click(button);
    expect(onLand).not.toHaveBeenCalled();
    expect(onAskAgent).not.toHaveBeenCalled();
    const explainer = screen.getByTestId("outside-explainer");
    expect(explainer.textContent).toContain("Changes outside this session are holding Land back.");
    fireEvent.click(within(explainer).getByRole("button", { name: "Ask the agent" }));
    expect(onAskAgent).toHaveBeenCalledWith(expect.stringContaining("Commit the ones that belong to this work"));
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("still offers Commit… first when the session's own files are uncommitted too", () => {
    renderDock({ ...fixtureIsolatedSession, outside_count: 2 });
    expect(control()!.getAttribute("data-work-state")).toBe("commit");
  });

  it("ignores the outside count for a session working in place", () => {
    renderDock({
      ...fixtureIsolatedSession,
      worktree_dir: undefined,
      worktree_branch: undefined,
      worktree_base: undefined,
      uncommitted_count: 0,
      outside_count: 2,
    });
    expect(control()).toBeNull();
  });

  it("re-renders from a live record update", () => {
    const work = { onCommit: vi.fn(), onLand: vi.fn() };
    const { rerender } = render(dock({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 }, work));
    expect(control()).toBeNull();
    rerender(dock({ ...fixtureIsolatedSession, uncommitted_count: 2 }, work));
    expect(control()!.textContent).toContain("2");
  });
});

describe("dock ⎇ popover", () => {
  beforeEach(() => {
    resetProviderModelCache();
    resetProviderInfoCache();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("lists folder, worktree, branch → base and status, with Commit… and Land into base", () => {
    const { onCommit } = renderDock(fixtureIsolatedSession);
    fireEvent.click(screen.getByTestId("dock-location"));
    const pop = screen.getByTestId("work-popover");
    expect(pop.textContent).toContain("/home/user/repo/mads");
    expect(pop.textContent).toContain("/home/user/.mads/session-worktrees/session-isolated");
    expect(pop.textContent).toContain("mads/session/session-isolated → main");
    expect(screen.getByTestId("work-status").textContent).toBe("3 commits to land · 4 files uncommitted");

    // Land would only be refused over uncommitted files: held, with why.
    const land = within(pop).getByRole("button", { name: "Land into main" }) as HTMLButtonElement;
    expect(land.disabled).toBe(true);

    fireEvent.click(within(pop).getByRole("button", { name: "Commit…" }));
    expect(onCommit).toHaveBeenCalled();
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("says when there's nothing to do, and toggles closed from the segment", () => {
    renderDock({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 });
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(screen.getByTestId("work-status").textContent).toBe("Nothing to commit or land");
    const commit = within(screen.getByTestId("work-popover")).getByRole("button", { name: "Commit…" });
    expect((commit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("has no Land button for a session working in place", () => {
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 1 });
    fireEvent.click(screen.getByTestId("dock-location"));
    const pop = screen.getByTestId("work-popover");
    expect(within(pop).queryByRole("button", { name: /Land/ })).toBeNull();
    expect(pop.textContent).not.toContain("Worktree");
  });

  it("opens on a status-line Land click and shows that Land's refusal there, with Ask the agent", () => {
    const onAskAgent = vi.fn();
    const work = { onCommit: vi.fn(), onLand: vi.fn(), onAskAgent };
    const { rerender } = render(dock(clean, work));
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    expect(work.onLand).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("work-popover")).toBeTruthy();
    expect(screen.queryByTestId("land-outcome")).toBeNull();

    rerender(dock(clean, { ...work, landing: true }));
    expect(screen.getByTestId("land-outcome").getAttribute("data-kind")).toBe("landing");

    rerender(dock(clean, work, refusedOutside));
    const outcome = screen.getByTestId("land-outcome");
    expect(outcome.getAttribute("data-kind")).toBe("refused");
    expect(outcome.getAttribute("data-reason")).toBe("dirty");
    expect(outcome.textContent).toContain("Changes outside this session are holding Land back");
    expect(outcome.textContent).toContain("ui/e2e/sessions.spec.ts and setup_dock.sh");

    fireEvent.click(within(outcome).getByRole("button", { name: "Ask the agent" }));
    expect(onAskAgent).toHaveBeenCalledWith(
      expect.stringContaining("These files changed outside this session's change list: ui/e2e/sessions.spec.ts, setup_dock.sh."),
    );
  });

  it("doesn't pass off the previous Land's outcome as this one's, but counts an identical repeat", () => {
    const work = { onCommit: vi.fn(), onLand: vi.fn() };
    const { rerender } = render(dock(clean, work, refusedOutside));
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    expect(screen.queryByTestId("land-outcome")).toBeNull();

    rerender(dock(clean, work, { ...refusedOutside, repeats: 2 }));
    expect(screen.getByTestId("land-outcome").getAttribute("data-kind")).toBe("refused");
    // A retryable refusal offers Land again from the popover.
    fireEvent.click(within(screen.getByTestId("land-outcome")).getByRole("button", { name: "Land again" }));
    expect(work.onLand).toHaveBeenCalledTimes(2);
  });

  it("shows nothing about old outcomes when opened from the ⎇ segment", () => {
    renderDock(clean, {}, refusedOutside);
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(screen.getByTestId("work-popover")).toBeTruthy();
    expect(screen.queryByTestId("land-outcome")).toBeNull();
  });

  it("renders a conflict with Ask the agent to resolve", () => {
    const onAskAgent = vi.fn();
    const work = { onCommit: vi.fn(), onLand: vi.fn(), onAskAgent };
    const { rerender } = render(dock(clean, work));
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    rerender(
      dock(clean, work, { kind: "conflicted", id: "wt-3", seq: 3, base: "main", files: ["a.go"], latest: true }),
    );
    const outcome = screen.getByTestId("land-outcome");
    expect(outcome.textContent).toContain("main moved on: 1 file conflicts.");
    fireEvent.click(within(outcome).getByRole("button", { name: "Ask the agent to resolve" }));
    expect(onAskAgent).toHaveBeenCalledWith(expect.stringContaining("Merge main into this branch"));
  });

  it("renders a successful Land", () => {
    const work = { onCommit: vi.fn(), onLand: vi.fn() };
    const { rerender } = render(dock(clean, work));
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    rerender(
      dock({ ...clean, unlanded_count: 0 }, work, {
        kind: "landed",
        id: "land-echo-x",
        seq: Number.POSITIVE_INFINITY,
        base: "main",
        mergeCommit: "abcdef1234567",
        fastForwarded: true,
      }),
    );
    const outcome = screen.getByTestId("land-outcome");
    expect(outcome.getAttribute("data-kind")).toBe("landed");
    expect(outcome.textContent).toBe("Landed into main. Merge commit abcdef1. This session keeps going on top of it.");
  });

  it("words a busy refusal from worktreeCopy, without Land again", () => {
    const work = { onCommit: vi.fn(), onLand: vi.fn() };
    const { rerender } = render(dock(clean, work));
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    rerender(
      dock(clean, work, { kind: "refused", id: "wt-4", seq: 4, base: "main", reason: "busy", latest: true }),
    );
    const outcome = screen.getByTestId("land-outcome");
    expect(outcome.textContent).toContain("Can't land while the agent is working");
    expect(within(outcome).queryByRole("button", { name: "Land again" })).toBeNull();
  });
});
