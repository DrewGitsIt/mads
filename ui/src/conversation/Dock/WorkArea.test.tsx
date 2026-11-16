import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { SessionDock, type DockWork } from "./SessionDock";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import { resetProviderModelCache } from "../../router/useProviderModels";
import { resetProviderInfoCache } from "../../router/useProviderInfos";
import { OPEN_SESSION_EVENT } from "../../appNavigation";
import type { Session, WorktreeStatus } from "../../router/types";
import type { LandRepo, PushFacts } from "../Worktree/workState";

/** The repo state store's answer for one repo with `commits` to push (pushFacts). */
const toPush = (commits: number, target: "branch" | "base" = "branch"): PushFacts => ({
  commits,
  repos: commits > 0 ? [{ root: "/r", primary: true, target, commits }] : [],
});

function dock(session: Session, work: Partial<DockWork> = {}) {
  return (
    <SessionDock
      session={session}
      workspaceName="mads"
      mode="chat"
      onModeChange={vi.fn()}
      work={{ onCommit: work.onCommit ?? vi.fn(), onLand: work.onLand ?? vi.fn(), ...work }}
    />
  );
}

function renderDock(session: Session, work: Partial<DockWork> = {}) {
  const onCommit = work.onCommit ?? vi.fn();
  const onLand = work.onLand ?? vi.fn();
  const utils = render(dock(session, { ...work, onCommit, onLand }));
  return { ...utils, onCommit, onLand };
}

const control = () => document.querySelector("[data-work-state]") as HTMLButtonElement | null;

const NOT_ISOLATED =
  "This session works directly in your checkout, so there is nothing to land. Commit is all it needs.";

function inPlaceStatus(overrides: Partial<WorktreeStatus> = {}): WorktreeStatus {
  return {
    isolated: false,
    folder: "/home/user/repo/mads",
    files: [],
    uncommitted_count: 1,
    unlanded_count: 0,
    land_blocked: { reason: "not_isolated", detail: NOT_ISOLATED },
    ...overrides,
  };
}
const clean = { ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 1 };

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

  it("opens the Commit dialog from Changes outside when it can, with Commit them… in the explainer (D6)", () => {
    const onCommitDialog = vi.fn();
    const onAskAgent = vi.fn();
    renderDock({ ...clean, unlanded_count: 3, outside_count: 5 }, { onCommitDialog, onAskAgent });
    const button = control()!;
    expect(button.getAttribute("title")).toMatch(/Commit can take them as changes by someone else\. Click to choose what to commit\.$/);
    fireEvent.click(button);
    expect(onCommitDialog).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("work-popover")).toBeNull();

    fireEvent.click(screen.getByTestId("dock-location"));
    const explainer = screen.getByTestId("outside-explainer");
    expect(within(explainer).getByRole("button", { name: "Ask the agent" })).toBeTruthy();
    fireEvent.click(within(explainer).getByRole("button", { name: "Commit them…" }));
    expect(onCommitDialog).toHaveBeenCalledTimes(2);
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

  it("counts the repos when uncommitted files span more than one (COMMIT-LAND-PUSH D3)", () => {
    const { onCommit } = renderDock({
      ...fixtureSessions["session-active"],
      uncommitted_count: 11,
      repos: [
        { root: "/home/user/repo/mads", uncommitted_count: 9 },
        { root: "/home/user/repo/learnanywhere", uncommitted_count: 2 },
      ],
    });
    const button = control()!;
    expect(button.getAttribute("data-work-state")).toBe("commit");
    expect(button.textContent).toBe("Commit… 11 · 2 repos");
    expect(button.getAttribute("title")).toBe("Commit 11 files in 2 repos");
    fireEvent.click(button);
    expect(onCommit).toHaveBeenCalled();
  });

  it("doesn't count a listed repo with nothing uncommitted", () => {
    renderDock({
      ...fixtureIsolatedSession,
      unlanded_count: 0,
      repos: [
        { root: "/home/user/.mads/session-worktrees/session-isolated", primary: true, isolated: true, uncommitted_count: 4 },
        { root: "/home/user/repo/learnanywhere", unlanded_count: 0 },
      ],
    });
    expect(control()!.textContent).toBe("Commit… 4");
  });

  it("offers Push ↑N once everything is committed, and opens the confirmation on its target", () => {
    const onPush = vi.fn();
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 0 }, { onPush, push: toPush(2) });
    const button = control()!;
    expect(button.getAttribute("data-work-state")).toBe("push");
    expect(button.textContent).toBe("Push ↑2");
    expect(button.getAttribute("title")).toBe("Push 2 commits");
    fireEvent.click(button);
    expect(onPush).toHaveBeenCalledWith("branch");
  });

  it("pushes an isolated session's base after Land", () => {
    const onPush = vi.fn();
    renderDock({ ...clean, unlanded_count: 0 }, { onPush, push: toPush(1, "base") });
    expect(control()!.getAttribute("title")).toBe("Push 1 commit");
    fireEvent.click(control()!);
    expect(onPush).toHaveBeenCalledWith("base");
  });

  it("has no Push without a push handler", () => {
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 0 }, { push: toPush(2) });
    expect(control()).toBeNull();
  });

  it("offers no Push while the repo state is unknown, rather than 0 then N", () => {
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 0 }, { onPush: vi.fn() });
    expect(control()).toBeNull();
  });

  it("shows Commit and Push side by side when both apply (D11), Commit first", () => {
    const onPush = vi.fn();
    const { onCommit } = renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 2 }, { onPush, push: toPush(3) });
    const controls = [...screen.getByTestId("dock-status").querySelectorAll<HTMLButtonElement>("[data-work-state]")];
    expect(controls.map((b) => b.getAttribute("data-work-state"))).toEqual(["commit", "push"]);
    expect(controls[0].textContent).toBe("Commit… 2");
    expect(controls[1].textContent).toBe("Push ↑3");
    expect(controls[1].getAttribute("aria-label")).toBe("Push 3 commits");
    fireEvent.click(controls[0]);
    expect(onCommit).toHaveBeenCalled();
    fireEvent.click(controls[1]);
    expect(onPush).toHaveBeenCalledWith("branch");
  });

  it("offers no Push beside Commit while commits wait to land: that push comes after Land", () => {
    renderDock({ ...fixtureIsolatedSession, uncommitted_count: 1, unlanded_count: 2 }, { onPush: vi.fn(), push: toPush(3, "base") });
    expect(screen.getByTestId("dock-status").querySelector('[data-work-state="push"]')).toBeNull();
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

  it("lists folder, worktree and branch → base, then where the work goes: Commit, then Land", () => {
    const { onCommit } = renderDock(fixtureIsolatedSession);
    fireEvent.click(screen.getByTestId("dock-location"));
    const pop = screen.getByTestId("work-popover");
    expect(pop.textContent).toContain("/home/user/repo/mads");
    expect(pop.textContent).toContain("/home/user/.mads/session-worktrees/session-isolated");
    expect(pop.textContent).toContain("mads/session/session-isolated → main");

    const path = within(pop).getByTestId("work-path");
    expect(path.textContent).toContain("Where this session's work goes");
    const commitStep = within(path).getByTestId("work-step-commit");
    expect(commitStep.getAttribute("data-state")).toBe("next");
    expect(commitStep.textContent).toContain("4 uncommitted");
    const landStep = within(path).getByTestId("work-step-land");
    expect(landStep.getAttribute("data-state")).toBe("wait");
    expect(landStep.textContent).toContain("Land into main");
    expect(landStep.textContent).toContain("3 commits on mads/session/session-isolated · lands as one commit on main");

    // Land would only be refused over uncommitted files: held, with why.
    const land = within(landStep).getByRole("button", { name: "Land" }) as HTMLButtonElement;
    expect(land.disabled).toBe(true);
    expect(land.title).toBe("Commit this session's changes first. Land only moves committed work.");

    fireEvent.click(within(commitStep).getByRole("button", { name: "Commit…" }));
    expect(onCommit).toHaveBeenCalled();
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("lands from the path once everything is committed", () => {
    const { onLand } = renderDock(clean);
    fireEvent.click(screen.getByTestId("dock-location"));
    const path = screen.getByTestId("work-path");
    const commitStep = within(path).getByTestId("work-step-commit");
    expect(commitStep.getAttribute("data-state")).toBe("done");
    expect(commitStep.textContent).toContain("all committed");
    expect(within(commitStep).queryByRole("button")).toBeNull();
    const landStep = within(path).getByTestId("work-step-land");
    expect(landStep.getAttribute("data-state")).toBe("next");
    fireEvent.click(within(landStep).getByRole("button", { name: "Land" }));
    expect(onLand).toHaveBeenCalledTimes(1);
  });

  it("says the work is done when there's nothing to do, and toggles closed from the segment", () => {
    renderDock({ ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 });
    fireEvent.click(screen.getByTestId("dock-location"));
    const path = screen.getByTestId("work-path");
    expect(within(path).getByTestId("work-step-commit").getAttribute("data-state")).toBe("done");
    expect(within(path).getByTestId("work-step-land").getAttribute("data-state")).toBe("done");
    expect(within(path).queryAllByRole("button")).toHaveLength(0);
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("has no Land step for a session working in place", () => {
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 1 });
    fireEvent.click(screen.getByTestId("dock-location"));
    const pop = screen.getByTestId("work-popover");
    expect(within(pop).queryByTestId("work-step-land")).toBeNull();
    expect(within(pop).queryByRole("button", { name: /Land/ })).toBeNull();
    expect(pop.textContent).not.toContain("Worktree");
  });

  it("pushes from the path and closes the popover", () => {
    const onPush = vi.fn();
    renderDock({ ...clean, uncommitted_count: 0 }, { onPush });
    fireEvent.click(screen.getByTestId("dock-location"));
    const step = screen.getByTestId("work-step-push");
    fireEvent.click(within(step).getByRole("button", { name: "Push branch" }));
    expect(onPush).toHaveBeenCalledWith("branch");
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("sums a session's repos in the path", () => {
    renderDock({
      ...fixtureSessions["session-active"],
      uncommitted_count: 11,
      repos: [
        { root: "/home/user/repo/mads", uncommitted_count: 9 },
        { root: "/home/user/repo/learnanywhere", uncommitted_count: 2 },
      ],
    });
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(screen.getByTestId("work-step-commit").textContent).toContain("11 uncommitted in 2 repos");
  });

  it("says why a session working in place has no Land, asking only once the popover opens", async () => {
    const loadStatus = vi.fn().mockResolvedValue(inPlaceStatus());
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 1 }, { loadStatus });
    expect(loadStatus).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("dock-location"));
    const note = await screen.findByTestId("no-land-note");
    expect(note.textContent?.trim()).toBe(NOT_ISOLATED);
    expect(within(note).queryByRole("button")).toBeNull();
    expect(loadStatus).toHaveBeenCalledTimes(1);
  });

  it("names the session whose worktree it works in, and opens it", async () => {
    const opened = vi.fn();
    const onOpen = (e: Event) => opened((e as CustomEvent<{ sessionId: string }>).detail.sessionId);
    window.addEventListener(OPEN_SESSION_EVENT, onOpen);
    try {
      const loadStatus = vi.fn().mockResolvedValue(
        inPlaceStatus({
          owner_session: { id: "ses_owner", name: "Dock redesign" },
          land_blocked: { reason: "not_isolated", detail: "This session works in another session's worktree." },
        }),
      );
      renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 1 }, { loadStatus });
      fireEvent.click(screen.getByTestId("dock-location"));
      const note = await screen.findByTestId("no-land-note");
      expect(note.textContent).toContain(
        "Changes here land from Dock redesign. This session works in another session's worktree.",
      );
      fireEvent.click(within(note).getByRole("button", { name: "Open Dock redesign" }));
      expect(opened).toHaveBeenCalledWith("ses_owner");
      expect(screen.queryByTestId("work-popover")).toBeNull();
    } finally {
      window.removeEventListener(OPEN_SESSION_EVENT, onOpen);
    }
  });

  it("doesn't ask an isolated session's status — its Land explains itself", () => {
    const loadStatus = vi.fn().mockResolvedValue(inPlaceStatus());
    renderDock(fixtureIsolatedSession, { loadStatus });
    fireEvent.click(screen.getByTestId("dock-location"));
    expect(loadStatus).not.toHaveBeenCalled();
    expect(screen.queryByTestId("no-land-note")).toBeNull();
  });

  it("opens the Land confirmation from the status line, never landing on the click (D12)", () => {
    const { onLand } = renderDock(clean);
    fireEvent.click(screen.getByRole("button", { name: /Land into main/ }));
    expect(onLand).toHaveBeenCalledTimes(1);
    expect(onLand).toHaveBeenCalledWith(undefined);
    // The confirmation is the caller's dialog; the popover stays shut.
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("gives a linked worktree with work to land its own Land step, and lands it from there", () => {
    const nested: LandRepo = { repo: "/home/user/repo/mads-nested", root: "/home/user/repo/mads-nested", branch: "nested-delegates", base: "main", commits: 1 };
    const inPlace = { ...fixtureSessions["session-active"], uncommitted_count: 0 };
    const { onLand } = renderDock(inPlace, { land: [nested] });
    // The status line's next step is Land too.
    const control = screen.getByTestId("dock-status").querySelector<HTMLButtonElement>('[data-work-state="land"]')!;
    expect(control.textContent).toBe("Land into main ↑1");
    fireEvent.click(screen.getByTestId("dock-location"));
    const step = screen
      .getAllByTestId("work-step-land")
      .find((s) => s.getAttribute("data-branch") === "nested-delegates")!;
    expect(step.textContent).toContain("Land nested-delegates into main");
    expect(step.textContent).toContain("1 commit");
    expect(step.getAttribute("data-state")).toBe("next");
    fireEvent.click(within(step).getByRole("button", { name: "Land" }));
    expect(onLand).toHaveBeenCalledWith(nested);
    expect(screen.queryByTestId("work-popover")).toBeNull();
  });

  it("sums every branch's work to land, without a base when they land into different ones", () => {
    const a: LandRepo = { repo: "/r/a", root: "/r/a", branch: "a", base: "main", commits: 2 };
    const b: LandRepo = { repo: "/r/b", root: "/r/b", branch: "b", base: "release", commits: 1 };
    renderDock({ ...fixtureSessions["session-active"], uncommitted_count: 0 }, { land: [a, b] });
    const control = screen.getByTestId("dock-status").querySelector<HTMLButtonElement>('[data-work-state="land"]')!;
    expect(control.textContent).toBe("Land ↑3");
    expect(control.getAttribute("title")).toBe("Land 3 commits from 2 branches");
  });
});
