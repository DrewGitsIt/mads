import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Launchpad, differsFromSaved, recentDirs, withStagedDefaults } from "./Launchpad";
import { OPEN_SESSION_EVENT } from "../../../appNavigation";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { fixtureConfig as DEFAULT_CONFIG } from "../../../router/fixtures";
import type { Config, Session, SessionDefaults, Workspace } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "New session",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const ws: Workspace = {
  id: "wsp_1",
  name: "core",
  repos: [{ path: "~/repo/core", primary: true }],
  defaults: {},
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
};

function defaults(overrides: Partial<SessionDefaults> = {}): SessionDefaults {
  return {
    provider: { value: "claude-code", source: "config" },
    model: {},
    agent: {},
    isolation: { value: "worktree", source: "builtin" },
    trust: { value: "limited", source: "config" },
    dir: { value: "/home/me/repo/mads", source: "builtin" },
    dir_status: "git",
    ...overrides,
  };
}

/**
 * A tiny Router: GET/PUT /config against one mutable Config, and
 * /sessions/defaults answered per requested ?dir= by `preview`.
 */
function stubRouter(
  preview: (dir: string | null, saved: Config) => SessionDefaults,
  config: Config,
) {
  const state = { config, puts: [] as Config[] };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
      const url = new URL(input.toString());
      const json = (body: unknown) =>
        Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
      if (url.pathname.endsWith("/sessions/defaults")) {
        return json(preview(url.searchParams.get("dir"), state.config));
      }
      if (url.pathname.endsWith("/config")) {
        if (init?.method === "PUT") {
          state.config = JSON.parse(init.body as string);
          state.puts.push(state.config);
        }
        return json(state.config);
      }
      if (url.pathname.endsWith("/workspaces")) return json([ws]);
      return json([]);
    }),
  );
  return state;
}

function typeFolder(path: string) {
  fireEvent.click(screen.getByText(/^folder$/).closest("button")!);
  const input = screen.getByLabelText("Folder path");
  fireEvent.change(input, { target: { value: path } });
  fireEvent.keyDown(input, { key: "Enter" });
}

describe("Launchpad folder + defaults", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("starts the session in any typed folder", async () => {
    stubRouter(() => defaults(), { ...DEFAULT_CONFIG });
    const createSession = vi.fn().mockResolvedValue(makeSession({}));
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/mads \(default\)/)).toBeTruthy());

    typeFolder("~/scratch/spike");
    expect(screen.getByText("spike")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Describe your goal"), { target: { value: "go" } });
    fireEvent.click(screen.getByText("Start session"));
    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ dir: "~/scratch/spike", workspace: undefined }),
      ),
    );
  });

  it("warns below the pills when a plain folder meets worktree isolation", async () => {
    stubRouter(
      (dir) =>
        dir
          ? defaults({ dir: { value: dir }, dir_status: "plain" })
          : defaults(),
      { ...DEFAULT_CONFIG },
    );
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/mads \(default\)/)).toBeTruthy());
    expect(screen.queryByRole("status")).toBeNull();

    typeFolder("/tmp/notes");
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toMatch(
        /\/tmp\/notes isn't a git repository/,
      ),
    );

    // Choosing to work in place answers the warning.
    fireEvent.click(screen.getByText(/New worktree \(default\)/));
    fireEvent.click(screen.getByText("Current directory"));
    await waitFor(() => expect(screen.queryByText(/isn't a git repository/)).toBeNull());
  });

  it("warns about a folder that isn't there, and won't save it as a default", async () => {
    stubRouter(
      (dir) => (dir ? defaults({ dir: { value: dir }, dir_status: "missing" }) : defaults()),
      { ...DEFAULT_CONFIG },
    );
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/mads \(default\)/)).toBeTruthy());

    typeFolder("/nope");
    await waitFor(() => expect(screen.getByText(/\/nope doesn't exist/)).toBeTruthy());
    expect(screen.queryByText("Save as my defaults")).toBeNull();
  });

  it("says whose worktree a folder is, and what each isolation does there", async () => {
    const wt = "/home/me/.mads/session-worktrees/ses_owner";
    stubRouter(
      (dir) =>
        dir === wt
          ? defaults({
              dir: { value: dir },
              dir_branch: "mads/session/ses_owner",
              dir_owner_session: { id: "ses_owner", name: "Dock redesign" },
            })
          : defaults(),
      { ...DEFAULT_CONFIG },
    );
    const opened = vi.fn();
    const onOpen = (e: Event) => opened((e as CustomEvent<{ sessionId: string }>).detail.sessionId);
    window.addEventListener(OPEN_SESSION_EVENT, onOpen);
    try {
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
      await waitFor(() => expect(screen.getByText(/mads \(default\)/)).toBeTruthy());
      expect(screen.queryByTestId("dir-owner-note")).toBeNull();

      typeFolder(wt);
      const note = await screen.findByTestId("dir-owner-note");
      expect(note.textContent).toBe(
        "This is Dock redesign’s worktree. A new worktree will branch from its branch, and Land goes back into Dock redesign’s worktree.",
      );

      fireEvent.click(screen.getByText(/New worktree \(default\)/));
      fireEvent.click(screen.getByText("Current directory"));
      await waitFor(() =>
        expect(screen.getByTestId("dir-owner-note").textContent).toBe(
          "This is Dock redesign’s worktree. You’ll share its branch; changes land from Dock redesign.",
        ),
      );

      fireEvent.click(screen.getAllByRole("button", { name: "Dock redesign" })[0]);
      expect(opened).toHaveBeenCalledWith("ses_owner");
    } finally {
      window.removeEventListener(OPEN_SESSION_EVENT, onOpen);
    }
  });

  it("saves every staged pill as the defaults in one PUT, then shows them as defaults", async () => {
    const state = stubRouter(
      (dir, saved) =>
        dir
          ? defaults({ dir: { value: dir } })
          : saved.default_dir
            ? defaults({
                dir: { value: saved.default_dir, source: "config" },
                trust: { value: saved.default_trust, source: "config" },
                isolation: { value: saved.default_isolation, source: "config" },
              })
            : defaults(),
      { ...DEFAULT_CONFIG, default_workspace: "wsp_old", default_provider: "claude-code" },
    );
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/mads \(default\)/)).toBeTruthy());
    expect(screen.queryByText("Save as my defaults")).toBeNull();

    typeFolder("/home/me/repo/site");
    fireEvent.click(screen.getByText(/limited|Limited/));
    fireEvent.click(screen.getByText("Full access"));
    fireEvent.click(screen.getByText(/New worktree \(default\)/));
    fireEvent.click(screen.getByText("Current directory"));

    await waitFor(() => expect(screen.getByText("Save as my defaults")).toBeTruthy());
    fireEvent.click(screen.getByText("Save as my defaults"));

    await waitFor(() => expect(screen.getByText("Saved as your defaults")).toBeTruthy());
    expect(state.puts).toHaveLength(1);
    expect(state.puts[0]).toMatchObject({
      default_dir: "/home/me/repo/site",
      default_workspace: "",
      default_trust: "full",
      default_isolation: "current_dir",
      // untouched pills keep what was saved
      default_provider: "claude-code",
    });
    // Staging cleared: the pills now read the saved values as defaults.
    await waitFor(() => expect(screen.getByText(/site \(default\)/)).toBeTruthy());
    expect(screen.getByText(/Full access \(default\)/)).toBeTruthy();
    expect(screen.getByText(/Current directory \(default\)/)).toBeTruthy();
  });
});

describe("differsFromSaved / withStagedDefaults", () => {
  const cfg: Config = { ...DEFAULT_CONFIG, default_dir: "/a", default_trust: "limited" };

  it("offers a save only for a pill that differs from what's saved", () => {
    expect(differsFromSaved({}, undefined, cfg)).toBe(false);
    expect(differsFromSaved({ dir: "/a" }, undefined, cfg)).toBe(false);
    expect(differsFromSaved({ dir: "/b" }, undefined, cfg)).toBe(true);
    expect(differsFromSaved({ trust: "limited" }, undefined, cfg)).toBe(false);
    expect(differsFromSaved({ trust: "full" }, undefined, cfg)).toBe(true);
    // Same folder, but a saved workspace would outrank it.
    expect(differsFromSaved({ dir: "/a" }, undefined, { ...cfg, default_workspace: "x" })).toBe(true);
  });

  it("treats folder and workspace as one pill when saving", () => {
    expect(withStagedDefaults(cfg, { workspace: "core" }, ws)).toMatchObject({
      default_workspace: "wsp_1",
      default_dir: "",
    });
    expect(
      withStagedDefaults({ ...cfg, default_workspace: "wsp_1" }, { dir: "/b" }, undefined),
    ).toMatchObject({ default_workspace: "", default_dir: "/b" });
  });

  it("saves a model together with the provider it belongs to", () => {
    expect(
      withStagedDefaults(cfg, { provider: "codex", model: "gpt-x" }, undefined),
    ).toMatchObject({ default_provider: "codex", default_model: "gpt-x" });
  });
});

describe("recentDirs", () => {
  it("lists each folder once, newest session first", () => {
    expect(
      recentDirs([
        makeSession({ dir: "/old", created_at: "2026-01-01T00:00:00Z" }),
        makeSession({ created_at: "2026-03-01T00:00:00Z" }),
        makeSession({ dir: "/new", created_at: "2026-02-01T00:00:00Z" }),
        makeSession({ dir: "/old", created_at: "2026-01-15T00:00:00Z" }),
      ]),
    ).toEqual(["/new", "/old"]);
  });
});

// The Launchpad unmounts whenever a session is open, so "+ New session"
// remounts it — the unsent goal and its staged settings must come back.
describe("Launchpad draft", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", FakeEventSource);
    stubRouter(() => defaults(), { ...DEFAULT_CONFIG });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function stageDraft() {
    await waitFor(() => expect(screen.getByText(/New worktree \(default\)/)).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the flaky deploy" },
    });
    fireEvent.click(screen.getByText(/New worktree \(default\)/));
    fireEvent.click(screen.getByText("Current directory"));
  }

  it("keeps the unsent goal and staged settings across a remount", async () => {
    const { unmount } = render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    await stageDraft();
    unmount();

    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    expect((screen.getByLabelText("Describe your goal") as HTMLTextAreaElement).value).toBe(
      "Fix the flaky deploy",
    );
    expect(screen.queryByText(/New worktree \(default\)/)).toBeNull();
    expect(screen.getByText(/Current directory/)).toBeTruthy();
  });

  it("starting the session clears the draft back to the defaults", async () => {
    const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s1" }));
    const { unmount } = render(
      <Launchpad createSession={createSession} onSessionCreated={vi.fn()} />,
    );
    await stageDraft();
    fireEvent.click(screen.getByText("Start session"));
    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ isolation: "current_dir" })),
    );
    unmount();

    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    expect((screen.getByLabelText("Describe your goal") as HTMLTextAreaElement).value).toBe("");
    await waitFor(() => expect(screen.getByText(/New worktree \(default\)/)).toBeTruthy());
  });

  it("a failed start keeps the draft for a retry", async () => {
    const createSession = vi.fn().mockRejectedValue(new Error("spawn failed"));
    const { unmount } = render(
      <Launchpad createSession={createSession} onSessionCreated={vi.fn()} />,
    );
    await stageDraft();
    fireEvent.click(screen.getByText("Start session"));
    await waitFor(() => expect(screen.getByText(/spawn failed/)).toBeTruthy());
    unmount();

    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    expect((screen.getByLabelText("Describe your goal") as HTMLTextAreaElement).value).toBe(
      "Fix the flaky deploy",
    );
  });
});
