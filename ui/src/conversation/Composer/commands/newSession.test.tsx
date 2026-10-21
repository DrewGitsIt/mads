import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { OPEN_CREATED_SESSION_EVENT, OPEN_SESSION_EVENT } from "../../../appNavigation";
import { setFlags } from "../../../featureFlags";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type {
  CreateSessionPayload,
  Link,
  Session,
  SessionValidation,
} from "../../../router/types";
import { newCommand } from "./new";
import { newFollowerCommand } from "./newFollower";
import { newLeaderCommand } from "./newLeader";
import { newMonitorCommand } from "./newMonitor";
import { stagedFromSession } from "./newSessionLinks";
import { availability, findCommand, matchCommands } from "./registry";
import type { CommandApi, CommandContext, CommandDescriptor } from "./types";
import { useLinkCommandState } from "./useLinkCommandState";

const THIS: Session = {
  id: "s-this",
  name: "dns-tracing",
  activity: "active",
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  dir: "/Users/me/repo/tracing",
  agent: "debugger",
  provider: "claude-code",
  model: "sonnet-5.5",
  trust: "auto_review",
  isolation: "worktree",
};

beforeEach(() => {
  vi.stubGlobal("EventSource", FakeEventSource);
  // The pills read the app's router (agents, workspaces, providers): empty.
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(() => Promise.resolve(new Response("[]", { status: 200 }))),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  setFlags({ multiRepo: false, linkedSessions: false, remoteRegistry: false });
});

function setup(ctx: Partial<CommandContext> = {}, validation?: Partial<SessionValidation>) {
  const client = new FakeRouterClient();
  const created: Session = { ...THIS, id: "s-new", name: "reproduce DNS error" };
  const createSession = vi
    .spyOn(client, "createSession")
    .mockResolvedValue({ session: created, addr: "" });
  const sendMessage = vi.spyOn(client, "sendMessage");
  const validateSession = vi
    .spyOn(client, "validateSession")
    .mockImplementation(async (p: CreateSessionPayload) => ({
      dir: p.dir ?? "",
      isolation: p.isolation ?? "worktree",
      errors: [],
      ...validation,
    }));
  let card: ((close: () => void) => ReactNode) | undefined;
  const close = vi.fn();
  const api = {
    ctx: {
      routerClient: client,
      sessionId: THIS.id,
      session: THIS,
      linkedSessions: true,
      ...ctx,
    },
    fail: vi.fn(),
    notify: vi.fn(),
    updateSession: vi.fn(),
    openCard: (r: (close: () => void) => ReactNode) => (card = r),
  } as CommandApi;
  async function open(command: CommandDescriptor, args = "reproduce DNS error") {
    await command.execute({ args }, api);
    render(<>{card!(close)}</>);
    // The debounced dry run has answered.
    await waitFor(() => expect(validateSession).toHaveBeenCalled());
  }
  return { client, api, close, createSession, sendMessage, validateSession, open, created };
}

/** FakeRouterClient.createSession declares no parameter; the card passes the payload. */
const payloadOf = (spy: { mock: { calls: unknown[][] } }) =>
  (spy.mock.calls[0] as [CreateSessionPayload])[0];

const goal = () => screen.getByLabelText("Goal for the new session") as HTMLTextAreaElement;
const startBtn = () => screen.getByRole("button", { name: /^Start\s*↵$/ }) as HTMLButtonElement;
const nameField = () => screen.getByLabelText("Name for the new session") as HTMLInputElement;
const openBtn = () => screen.getByRole("button", { name: /Start & open/ }) as HTMLButtonElement;

describe("/new prefill", () => {
  it("stages every pill from this session", () => {
    expect(stagedFromSession(THIS)).toEqual({
      dir: "/Users/me/repo/tracing",
      agent: "debugger",
      provider: "claude-code",
      model: "sonnet-5.5",
      trust: "auto_review",
      isolation: "worktree",
    });
    expect(stagedFromSession({ ...THIS, dir: undefined, workspace: "tracing-ws" })).toMatchObject({
      workspace: "tracing-ws",
    });
  });

  it("opens with the goal from the args and the pills showing this session's values", async () => {
    const { open } = setup();
    await open(newCommand);
    expect(goal().value).toBe("reproduce DNS error");
    expect(screen.getByRole("button", { name: /folder\s+tracing/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /persona\s+debugger/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /model\s+sonnet-5.5 · claude-code/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /isolation\s+New worktree/ })).toBeTruthy();
    // Plain /new has no relationship row.
    expect(screen.queryByText(/──/)).toBeNull();
  });
});

describe("payloads", () => {
  const base = {
    name: "reproduce DNS error",
    name_source: "derived",
    dir: THIS.dir,
    agent: THIS.agent,
    provider: THIS.provider,
    model: THIS.model,
    trust: THIS.trust,
    isolation: THIS.isolation,
  };

  it("/new on a full-trust-only provider sends Full, whatever this session ran at", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        if (input.toString().endsWith("/providers")) {
          const matrix = { trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" };
          return Promise.resolve(new Response(JSON.stringify([{ name: "hermes", matrix }]), { status: 200 }));
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    const { open, createSession } = setup({ session: { ...THIS, provider: "hermes", model: "copilot:gpt-4.1", trust: "limited" } });
    await open(newCommand);
    await waitFor(() =>
      expect(screen.getByTestId("trust-reason").textContent).toBe("Hermes can't enforce approvals; it runs at full trust"),
    );
    fireEvent.click(startBtn());
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession).trust).toBe("full");
  });

  it("/new creates with the staged pills and no links", async () => {
    const { open, createSession } = setup();
    await open(newCommand);
    fireEvent.click(startBtn());
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    const payload = payloadOf(createSession);
    expect(payload).toMatchObject(base);
    expect(payload.links).toBeUndefined();
  });

  it.each([
    [
      "/new-leader",
      newLeaderCommand,
      { granting_session_id: "$new", subject_session_id: "s-this", preset: "leader" },
      /✎\s*── leads ──▶\s*dns-tracing/,
    ],
    [
      "/new-follower",
      newFollowerCommand,
      { granting_session_id: "s-this", subject_session_id: "$new", preset: "leader" },
      /dns-tracing\s*this\s*── leads ──▶\s*✎/,
    ],
    [
      "/new-monitor",
      newMonitorCommand,
      { granting_session_id: "$new", subject_session_id: "s-this", preset: "monitor" },
      /✎\s*── watches ──▶\s*dns-tracing/,
    ],
  ])("%s stages its link in the same request, cadence on signal by default", async (_, command, link, row) => {
    const { open, createSession } = setup();
    await open(command);
    expect(screen.getByTestId("new-session-card").textContent).toMatch(row);
    // The new node is its name field, previewing the name the goal gives it.
    expect(nameField().placeholder).toBe("reproduce DNS error");
    fireEvent.click(startBtn());
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession)).toMatchObject({
      ...base,
      links: [{ ...link, cadence: "on_signal" }],
    });
  });

  it("the cadence switch is applied to the link", async () => {
    const { open, createSession, validateSession } = setup();
    await open(newFollowerCommand);
    fireEvent.click(screen.getByRole("radio", { name: "per turn" }));
    await waitFor(() =>
      expect(validateSession).toHaveBeenLastCalledWith(
        expect.objectContaining({ links: [expect.objectContaining({ cadence: "per_turn" })] }),
      ),
    );
    fireEvent.click(startBtn());
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession).links).toEqual([
      {
        granting_session_id: "s-this",
        subject_session_id: "$new",
        preset: "leader",
        cadence: "per_turn",
      },
    ]);
  });

  it("the arrow's tooltip says what the link allows", async () => {
    const { open } = setup();
    await open(newMonitorCommand);
    expect(screen.getByText(/── watches ──▶/).getAttribute("title")).toBe(
      "The new session will watch dns-tracing. It gets a digest of dns-tracing's activity but can't send it messages.",
    );
  });
});

describe("Enter vs ⌘Enter", () => {
  it("Enter in the goal starts in the background: closes, sends the goal, toasts with Open", async () => {
    const { open, close, createSession, sendMessage, api, created } = setup();
    const opened = vi.fn();
    window.addEventListener(OPEN_SESSION_EVENT, opened);
    await open(newCommand);
    fireEvent.keyDown(goal(), { key: "Enter" });
    expect(close).toHaveBeenCalled();
    await waitFor(() =>
      expect(api.notify).toHaveBeenCalledWith(`Started ${created.name}`, expect.anything()),
    );
    expect(createSession).toHaveBeenCalledTimes(1);
    expect(sendMessage).toHaveBeenCalledWith("s-new", { text: "reproduce DNS error", queue: true });
    const action = vi.mocked(api.notify).mock.calls[0][1]!;
    expect(action.label).toBe("Open");
    action.onClick();
    expect((opened.mock.calls[0][0] as CustomEvent).detail).toEqual({ sessionId: "s-new" });
    window.removeEventListener(OPEN_SESSION_EVENT, opened);
  });

  it("⌘Enter creates and opens the session the Launchpad way (its composer sends the goal)", async () => {
    const { open, close, createSession, sendMessage, created } = setup();
    const opened = vi.fn();
    window.addEventListener(OPEN_CREATED_SESSION_EVENT, opened);
    await open(newCommand);
    fireEvent.keyDown(goal(), { key: "Enter", metaKey: true });
    await waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    expect((opened.mock.calls[0][0] as CustomEvent).detail).toEqual({
      session: created,
      goal: "reproduce DNS error",
    });
    expect(createSession).toHaveBeenCalledTimes(1);
    expect(sendMessage).not.toHaveBeenCalled();
    expect(close).toHaveBeenCalled();
    window.removeEventListener(OPEN_CREATED_SESSION_EVENT, opened);
  });

  it("Shift+Enter is a newline, and an empty goal can't start", async () => {
    const { open, createSession } = setup();
    await open(newCommand, "");
    expect(startBtn().disabled).toBe(true);
    expect(openBtn().disabled).toBe(true);
    fireEvent.keyDown(goal(), { key: "Enter" });
    fireEvent.keyDown(goal(), { key: "Enter", shiftKey: true });
    expect(createSession).not.toHaveBeenCalled();
  });

  it("a refused ⌘Enter stays on the card with the reason", async () => {
    const { open, close, createSession } = setup();
    createSession.mockRejectedValueOnce(new Error("provider is not installed"));
    await open(newCommand);
    fireEvent.click(openBtn());
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toMatch(/provider is not installed/),
    );
    expect(close).not.toHaveBeenCalled();
  });
});

describe("naming and a goalless follower", () => {
  it("a typed name is kept as chosen, not derived from the goal", async () => {
    const { open, createSession } = setup();
    await open(newLeaderCommand);
    fireEvent.change(nameField(), { target: { value: "  dns lead  " } });
    fireEvent.keyDown(nameField(), { key: "Enter" });
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession)).toMatchObject({ name: "dns lead", name_source: "user" });
  });

  it("/new-leader and /new-monitor still need a goal", async () => {
    const { open } = setup();
    await open(newMonitorCommand, "");
    expect(startBtn().disabled).toBe(true);
    expect(nameField().placeholder).toBe("new session");
    expect(goal().placeholder).toBe("What should the new session do?");
  });

  it("/new-follower without a goal: Create makes 'follower of [this]', numbered past taken names, and sends nothing", async () => {
    const { client, open, createSession, sendMessage, api, created } = setup();
    vi.spyOn(client, "listSessions").mockResolvedValue([
      { ...THIS, id: "s-x", name: "follower of [dns-tracing]" },
    ]);
    await open(newFollowerCommand, "");
    expect(goal().placeholder).toBe(
      "Optional for new followers. Leave empty to let the leader agent start the dialogue.",
    );
    await waitFor(() => expect(nameField().placeholder).toBe("follower of [dns-tracing] 2"));
    const create = screen.getByRole("button", { name: /^Create\s*↵$/ }) as HTMLButtonElement;
    expect(screen.getByRole("button", { name: /Create & open/ })).toBeTruthy();
    expect(create.disabled).toBe(false);
    fireEvent.click(create);
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession)).toMatchObject({
      name: "follower of [dns-tracing] 2",
      name_source: "user",
    });
    expect(payloadOf(createSession).opening_prompt).toBeUndefined();
    await waitFor(() =>
      expect(api.notify).toHaveBeenCalledWith(`Created ${created.name}`, expect.anything()),
    );
    expect(sendMessage).not.toHaveBeenCalled();
  });

  it("/new-follower with a goal is a normal Start: derived name, goal sent", async () => {
    const { open, createSession, sendMessage } = setup();
    await open(newFollowerCommand);
    fireEvent.click(startBtn());
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1));
    expect(payloadOf(createSession)).toMatchObject({
      name: "reproduce DNS error",
      name_source: "derived",
    });
  });

  it("Create & open on a goalless follower opens it with nothing to send", async () => {
    const { open, sendMessage } = setup();
    const opened = vi.fn();
    window.addEventListener(OPEN_CREATED_SESSION_EVENT, opened);
    await open(newFollowerCommand, "");
    fireEvent.click(screen.getByRole("button", { name: /Create & open/ }));
    await waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    expect((opened.mock.calls[0][0] as CustomEvent).detail.goal).toBe("");
    expect(sendMessage).not.toHaveBeenCalled();
    window.removeEventListener(OPEN_CREATED_SESSION_EVENT, opened);
  });
});

describe("validation", () => {
  it("a joined counterpart worktree locks Isolation and shows the joined folder", async () => {
    const { open } = setup(
      {},
      {
        dir: "/Users/me/.mads/worktrees/s-this",
        isolation: "current_dir",
        joined_worktree_of: { session_id: "s-this", name: "dns-tracing" },
      },
    );
    await open(newMonitorCommand);
    const pill = await screen.findByRole("button", { name: /isolation\s+shares dns-tracing's worktree/ });
    expect(pill.getAttribute("aria-disabled")).toBe("true");
    expect(pill.getAttribute("title")).toMatch(/Linked sessions must share one copy of the repo/);
    const banner = screen.getByRole("status");
    expect(banner.textContent).toMatch(/dns-tracing has its own worktree/);
    expect(banner.textContent).toContain("/Users/me/.mads/worktrees/s-this");
    expect(startBtn().disabled).toBe(false);
  });

  it("a blocking error disables both Starts and shows the server's message", async () => {
    const { open, createSession } = setup(
      {},
      {
        errors: [
          {
            field: "links[0]",
            message: "the counterpart's worktree is missing — open it once to restore its folder",
          },
        ],
      },
    );
    await open(newFollowerCommand);
    await screen.findByText(/open it once to restore its folder/);
    expect(startBtn().disabled).toBe(true);
    expect(openBtn().disabled).toBe(true);
    fireEvent.keyDown(goal(), { key: "Enter" });
    fireEvent.keyDown(goal(), { key: "Enter", metaKey: true });
    expect(createSession).not.toHaveBeenCalled();
  });

  it("re-validates as pills change, the goal aside", async () => {
    const { open, validateSession } = setup();
    await open(newCommand);
    const calls = validateSession.mock.calls.length;
    fireEvent.change(goal(), { target: { value: "something else" } });
    await new Promise((r) => setTimeout(r, 300));
    expect(validateSession.mock.calls.length).toBe(calls);
  });
});

describe("/new-leader availability", () => {
  const ctx = (extra: Partial<CommandContext>): CommandContext => ({
    routerClient: new FakeRouterClient(),
    sessionId: THIS.id,
    session: THIS,
    linkedSessions: true,
    ...extra,
  });

  it("is disabled with the leader named when this session is already led", () => {
    expect(availability(newLeaderCommand, ctx({ leader: { id: "s-lead", name: "triage-lead" } }))).toEqual({
      ok: false,
      reason: "dns-tracing is already led by triage-lead",
    });
    expect(availability(newFollowerCommand, ctx({ leader: { id: "s-lead", name: "triage-lead" } })).ok).toBe(true);
  });

  it("is disabled for a pipeline run", () => {
    expect(availability(newLeaderCommand, ctx({ pipelineRunning: true }))).toEqual({
      ok: false,
      reason: "pipeline runs can't be led",
    });
    expect(availability(newMonitorCommand, ctx({ pipelineRunning: true })).ok).toBe(true);
  });

  it("forced anyway, the card opens blocked with the leader to open", async () => {
    const { open, createSession } = setup({ leader: { id: "s-lead", name: "triage-lead" } });
    await open(newLeaderCommand);
    const banner = screen.getByRole("alert");
    expect(banner.textContent).toMatch(/dns-tracing already has a leader \(triage-lead\)/);
    expect(startBtn().disabled).toBe(true);
    expect(openBtn().disabled).toBe(true);
    const opened = vi.fn();
    window.addEventListener(OPEN_SESSION_EVENT, opened);
    fireEvent.click(screen.getByRole("button", { name: "Open triage-lead" }));
    expect((opened.mock.calls[0][0] as CustomEvent).detail).toEqual({ sessionId: "s-lead" });
    window.removeEventListener(OPEN_SESSION_EVENT, opened);
    fireEvent.keyDown(goal(), { key: "Enter" });
    expect(createSession).not.toHaveBeenCalled();
  });
});

describe("feature flag", () => {
  it("hides the link variants when linked sessions are off; /new stays", () => {
    const off: CommandContext = { routerClient: new FakeRouterClient(), sessionId: "s", linkedSessions: false };
    expect(matchCommands("new", off).map((m) => m.command.name)).toEqual(["new"]);
    expect(findCommand("new-follower", off)).toBeUndefined();
    const on = { ...off, linkedSessions: true };
    expect(matchCommands("new", on).map((m) => m.command.name)).toEqual([
      "new",
      "new-leader",
      "new-follower",
      "new-monitor",
    ]);
  });
});

describe("useLinkCommandState", () => {
  const lead: Link = {
    id: "l1",
    granting_session_id: "s-lead",
    subject_session_id: "s-this",
    grants: ["read", "inject", "orchestrate"],
    cadence: "on_signal",
    created_at: "",
    updated_at: "",
  };

  it("names the session holding orchestrate over this one, and nothing with the flag off", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValue({ ...THIS, id: "s-lead", name: "triage-lead" });
    vi.spyOn(client, "metricsPipelineRuns").mockResolvedValue([]);
    const { result } = renderHook(() => useLinkCommandState(client, "s-this", [lead]));
    expect(result.current.linkedSessions).toBe(false);
    expect(result.current.leader).toBeUndefined();
    act(() => setFlags({ multiRepo: false, linkedSessions: true, remoteRegistry: false }));
    await waitFor(() => expect(result.current.leader).toEqual({ id: "s-lead", name: "triage-lead" }));
  });

  it("a monitor link is not a leader; a running pipeline run is reported", async () => {
    setFlags({ multiRepo: false, linkedSessions: true, remoteRegistry: false });
    const client = new FakeRouterClient();
    vi.spyOn(client, "metricsPipelineRuns").mockResolvedValue([
      {
        session_id: "s-this",
        run_seq: 1,
        started_at: "",
        outcome: "running",
        tasks_done: 0,
        tasks_blocked: 0,
        tasks_skipped: 0,
        cost_usd: 0,
      },
    ]);
    const monitor = { ...lead, grants: ["read" as const] };
    const { result } = renderHook(() => useLinkCommandState(client, "s-this", [monitor]));
    await waitFor(() => expect(result.current.pipelineRunning).toBe(true));
    expect(result.current.leader).toBeUndefined();
  });
});
