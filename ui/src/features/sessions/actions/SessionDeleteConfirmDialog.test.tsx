import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionDeleteConfirmDialog, keptSentence, sessionDeleteState } from "./SessionDeleteConfirmDialog";
import { SessionRail } from "../rail/SessionRail";
import { SessionWorkspace } from "../workspace/SessionWorkspace";
import { EMPTY_AGENT_STATE, EMPTY_OPEN_TASK_BREAKDOWN, statusOf } from "../rail/railGroups";
import type { OpenTaskBreakdown, SessionAgentState } from "../rail/railGroups";
import { FakeRouterClient, fixtureSessions } from "../../../router/fixtures";
import { FakePipelineClient } from "../../../pipeline/client/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "Ollama parallel inference tuning",
    activity: "inactive",
    created_at: "2026-07-01T12:00:00Z",
    updated_at: "2026-07-01T12:00:00Z",
    // Midday UTC: the calendar stamp below reads "1 Jul" in any plausible local zone.
    last_turn_at: "2026-07-01T12:00:00Z",
    ...overrides,
  };
}

/** A turn that started minutes ago — far enough from a second boundary that the elapsed bucket can't flip mid-test. */
function workingState(overrides: Partial<SessionAgentState> = {}): SessionAgentState {
  return {
    subagents: 0,
    workingSince: new Date(Date.now() - 5 * 60_000).toISOString(),
    label: "Running bash · make check",
    ...overrides,
  };
}

const PIPELINE: OpenTaskBreakdown = { running: 1, ready: 4, blocked: 1 };

/** The dialog's own two-step, as the component does it: the card's status line first, then which dialog that makes. */
function deleteStateOf(
  session: Session,
  agentState: SessionAgentState,
  breakdown: OpenTaskBreakdown,
) {
  return sessionDeleteState(statusOf(session, agentState, breakdown), session);
}

function renderDialog(props: Partial<Parameters<typeof SessionDeleteConfirmDialog>[0]> = {}) {
  render(
    <SessionDeleteConfirmDialog
      open
      session={makeSession()}
      onConfirm={vi.fn()}
      onCancel={vi.fn()}
      {...props}
    />,
  );
  return screen.getByRole("alertdialog");
}

function panelOf(dialog: HTMLElement): HTMLElement {
  return dialog.querySelector<HTMLElement>("[data-delete-state]")!;
}

describe("SessionDeleteConfirmDialog", () => {
  it("names the session in its title — the rail deletes sessions that aren't the one on screen", () => {
    const dialog = renderDialog({ session: makeSession({ name: "Fix 5 MADS UI issues" }) });
    expect(within(dialog).getByRole("heading").textContent).toBe("Delete “Fix 5 MADS UI issues”?");
  });

  it("falls back to the rail's own name for an unnamed session", () => {
    const dialog = renderDialog({ session: makeSession({ name: "" }) });
    expect(within(dialog).getByRole("heading").textContent).toBe("Delete “Untitled session”?");
  });

  it("quiet: a sleeping session keeps today's copy, plus a last-activity line and a plain Delete", () => {
    const dialog = renderDialog();
    const panel = panelOf(dialog);

    expect(panel.dataset.deleteState).toBe("quiet");
    expect(panel.dataset.deleteTone).toBe("neutral");
    expect(panel.textContent).toContain("Sleeping · nothing running");
    expect(panel.textContent).toContain("last activity 1 Jul");
    expect(dialog.textContent).toContain(
      "Removes its tasks, file-change history and commit records.",
    );
    expect(dialog.textContent).toContain(
      "Provider transcripts and the event log survive. This can't be undone.",
    );
    expect(within(dialog).getByRole("button", { name: "Delete" })).toBeTruthy();
  });

  it("open: an idle live session says the delete closes it first, and the button says so too", () => {
    const dialog = renderDialog({ session: makeSession({ activity: "active" }) });
    const panel = panelOf(dialog);

    expect(panel.dataset.deleteState).toBe("open");
    expect(panel.dataset.deleteTone).toBe("neutral");
    expect(panel.textContent).toContain("Open · waiting on you");
    expect(panel.textContent).toContain("Deleting closes it first.");
    expect(within(dialog).getByRole("button", { name: "Close and delete" })).toBeTruthy();
  });

  it("working: the live label and elapsed from the status line, and Stop and delete", () => {
    const session = makeSession({ activity: "active" });
    const dialog = renderDialog({ session, agentState: workingState() });
    const panel = panelOf(dialog);

    expect(panel.dataset.deleteState).toBe("working");
    expect(panel.dataset.deleteTone).toBe("work");
    expect(panel.textContent).toContain("Working right now — this will interrupt it");
    // Byte-identical to the clause the card's own status line is showing.
    expect(panel.textContent).toContain("Running bash · make check · 5m");
    expect(dialog.textContent).toContain("The turn in flight is lost.");
    expect(within(dialog).getByRole("button", { name: "Stop and delete" })).toBeTruthy();
  });

  it("working's label is the status line's, subagent roll-up and all", () => {
    const panel = panelOf(
      renderDialog({ agentState: workingState({ subagents: 2, label: "Running bash" }) }),
    );
    expect(panel.textContent).toContain("2 subagents running");
  });

  describe("precedence — working › open › quiet", () => {
    it("working beats open: a live session mid-turn gets the working dialog", () => {
      const session = makeSession({ activity: "active" });
      expect(deleteStateOf(session, workingState(), EMPTY_OPEN_TASK_BREAKDOWN)).toBe("working");
    });

    it("working beats quiet: a record that reads inactive but is mid-turn is not 'sleeping'", () => {
      const session = makeSession({ activity: "inactive" });
      expect(deleteStateOf(session, workingState(), EMPTY_OPEN_TASK_BREAKDOWN)).toBe("working");
    });

    it("open beats quiet, and quiet is what's left", () => {
      expect(
        deleteStateOf(
          makeSession({ activity: "active" }),
          EMPTY_AGENT_STATE,
          EMPTY_OPEN_TASK_BREAKDOWN,
        ),
      ).toBe("open");
      expect(deleteStateOf(makeSession(), EMPTY_AGENT_STATE, EMPTY_OPEN_TASK_BREAKDOWN)).toBe("quiet");
    });

    it("a card that says working can never open a dialog that says sleeping", () => {
      const session = makeSession({ activity: "inactive" });
      const state = workingState();
      expect(statusOf(session, state, EMPTY_OPEN_TASK_BREAKDOWN).kind).toBe("working");
      expect(deleteStateOf(session, state, EMPTY_OPEN_TASK_BREAKDOWN)).toBe("working");
    });
  });

  describe("the open-task count is additive on top of all three", () => {
    it("escalates a quiet session to the warn tone, with the breakdown and the count in the loss", () => {
      const dialog = renderDialog({ openTaskBreakdown: PIPELINE });
      const panel = panelOf(dialog);

      expect(panel.dataset.deleteState).toBe("quiet");
      expect(panel.dataset.deleteTone).toBe("attn");
      expect(panel.textContent).toContain("6 tasks · 1 running · 4 ready · 1 blocked");
      expect(dialog.textContent).toContain(
        "Removes 6 tasks, plus its file-change history and commit records.",
      );
      // The "nothing running" claim goes away rather than contradicting the line below it.
      expect(panel.textContent).not.toContain("nothing running");
      expect(within(dialog).getByRole("button", { name: "Delete" })).toBeTruthy();
    });

    it("escalates an open session, leaving its state and button alone", () => {
      const dialog = renderDialog({
        session: makeSession({ activity: "active" }),
        openTaskBreakdown: PIPELINE,
      });
      const panel = panelOf(dialog);

      expect(panel.dataset.deleteState).toBe("open");
      expect(panel.dataset.deleteTone).toBe("attn");
      expect(panel.textContent).toContain("Deleting closes it first.");
      expect(panel.textContent).toContain("6 tasks · 1 running · 4 ready · 1 blocked");
      expect(within(dialog).getByRole("button", { name: "Close and delete" })).toBeTruthy();
    });

    it("escalates a working session past the accent tone, and names the turn in flight", () => {
      const dialog = renderDialog({
        session: makeSession({ activity: "active" }),
        agentState: workingState(),
        openTaskBreakdown: PIPELINE,
      });
      const panel = panelOf(dialog);

      expect(panel.dataset.deleteState).toBe("working");
      expect(panel.dataset.deleteTone).toBe("attn");
      expect(panel.textContent).toContain("Running bash · make check · 5m");
      expect(panel.textContent).toContain("6 tasks · 1 running · 4 ready · 1 blocked");
      expect(dialog.textContent).toContain(
        "Removes 6 tasks including the one in flight, plus its file-change history and commit records.",
      );
      expect(within(dialog).getByRole("button", { name: "Stop and delete" })).toBeTruthy();
    });

    it("lists only the non-empty buckets, and counts one task as a task", () => {
      const panel = panelOf(renderDialog({ openTaskBreakdown: { running: 0, ready: 1, blocked: 0 } }));
      expect(panel.textContent).toContain("1 task · 1 ready");
      expect(panel.textContent).not.toContain("running");
      expect(panel.textContent).not.toContain("blocked");
    });
  });

  it("renders nothing while closed, so its elapsed clock isn't running either", () => {
    render(
      <SessionDeleteConfirmDialog
        open={false}
        session={makeSession()}
        agentState={workingState()}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("the rail and the workspace kebab render the same text for the same session", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("is byte-identical, down to the live label and the task count", async () => {
    const session = fixtureSessions["session-active"];
    const agentState = workingState();

    const rail = render(
      <SessionRail
        sessions={[session]}
        openTaskCounts={{ [session.id]: PIPELINE }}
        agentStates={{ [session.id]: agentState }}
        onDelete={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: `Archive ${session.name}` }));
    const fromRail = screen.getByRole("alertdialog").textContent;
    rail.unmount();

    const workspace = render(
      <SessionWorkspace
        sessionId={session.id}
        routerClient={new FakeRouterClient()}
        pipelineClient={new FakePipelineClient()}
        openTaskBreakdown={PIPELINE}
        agentState={agentState}
        onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    const fromWorkspace = screen.getByRole("alertdialog").textContent;
    workspace.unmount();

    expect(fromRail).toContain("Working right now");
    expect(fromRail).toContain("6 tasks · 1 running · 4 ready · 1 blocked");
    expect(fromWorkspace).toBe(fromRail);
  });
});

describe("SessionDeleteConfirmDialog — an isolated session's work (D7)", () => {
  const isolated = makeSession({
    worktree_dir: "/home/u/.mads/session-worktrees/s1",
    worktree_branch: "mads/session/s1",
    worktree_base: "main",
    uncommitted_count: 0,
    unlanded_count: 0,
  });

  it("recounts at open and says what deleting discards", async () => {
    const client = new FakeRouterClient();
    client.worktreeStatus = { ...client.worktreeStatus, unlanded_count: 3, uncommitted_count: 2 };
    render(<SessionDeleteConfirmDialog open session={isolated} routerClient={client} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(await screen.findByText("Work that isn't in main")).toBeTruthy();
    expect(screen.getByText("3 unlanded commits and 2 uncommitted files will be discarded.")).toBeTruthy();
  });

  it("names only what there is", async () => {
    const client = new FakeRouterClient();
    client.worktreeStatus = { ...client.worktreeStatus, unlanded_count: 1, uncommitted_count: 0 };
    render(<SessionDeleteConfirmDialog open session={isolated} routerClient={client} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(await screen.findByText("1 unlanded commit will be discarded.")).toBeTruthy();
  });

  it("says nothing when the work is all landed", async () => {
    const client = new FakeRouterClient();
    client.worktreeStatus = { ...client.worktreeStatus, unlanded_count: 0, uncommitted_count: 0 };
    const spy = vi.spyOn(client, "getWorktreeStatus");
    render(<SessionDeleteConfirmDialog open session={isolated} routerClient={client} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    await waitFor(() => expect(spy).toHaveBeenCalledWith("s1"));
    expect(screen.queryByText(/will be discarded/)).toBeNull();
  });

  it("says the folder stays while a joined session works in it", async () => {
    const client = new FakeRouterClient();
    client.worktreeStatus = {
      ...client.worktreeStatus,
      unlanded_count: 2,
      shared_with: [{ id: "s2", name: "Reviewer" }],
    };
    render(<SessionDeleteConfirmDialog open session={isolated} routerClient={client} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(await screen.findByText("Its folder stays: “Reviewer” is working in it.")).toBeTruthy();
    expect(screen.getByText("Its folder and branch mads/session/s1 are kept.")).toBeTruthy();
    expect(screen.queryByText(/will be discarded/)).toBeNull();
  });

  it("says the folder stays while a pipeline run merges into it", async () => {
    const client = new FakeRouterClient();
    client.worktreeStatus = { ...client.worktreeStatus, uncommitted_count: 1, pipeline_running: true };
    render(<SessionDeleteConfirmDialog open session={isolated} routerClient={client} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(await screen.findByText("Its folder stays: a pipeline run is still merging into it.")).toBeTruthy();
    expect(screen.queryByText(/will be discarded/)).toBeNull();
  });

  it("names every session working in the folder", () => {
    expect(
      keptSentence(
        [
          { id: "a", name: "A" },
          { id: "b", name: "" },
          { id: "c", name: "C" },
        ],
        true,
      ),
    ).toBe("Its folder stays: “A”, “Untitled session” and “C” are working in it.");
  });

  it("doesn't ask git about a session working in place — its folder isn't deleted", () => {
    const client = new FakeRouterClient();
    const spy = vi.spyOn(client, "getWorktreeStatus");
    render(
      <SessionDeleteConfirmDialog
        open
        session={makeSession({ uncommitted_count: 2 })}
        routerClient={client}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(spy).not.toHaveBeenCalled();
    expect(screen.queryByText(/will be discarded/)).toBeNull();
  });
});
