import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Launchpad, deriveSessionName } from "./Launchpad";
import { FLAGS } from "../../../featureFlags";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { FakeRouterClient } from "../../../router/fixtures";
import { stagedForSession } from "../../../components/Attachments/stagedAttachments";
import type { Session, SessionDefaults } from "../../../router/types";

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

function stubEmptyFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(() => Promise.resolve(new Response("[]", { status: 200 }))),
  );
}

function stubFetchWithSessionDefaults(defaults: SessionDefaults) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.includes("/sessions/defaults")) {
        return Promise.resolve(new Response(JSON.stringify(defaults), { status: 200 }));
      }
      return Promise.resolve(new Response("[]", { status: 200 }));
    }),
  );
}

describe("deriveSessionName", () => {
  it("uses the first line, in full, with no truncation", () => {
    expect(deriveSessionName("Fix the deploy script\nmore detail")).toBe("Fix the deploy script");
    expect(deriveSessionName("x".repeat(80))).toBe("x".repeat(80));
  });
});

describe("Launchpad", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    stubEmptyFetch();
    URL.createObjectURL = vi.fn(() => "blob:mock-url");
    URL.revokeObjectURL = vi.fn();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("disables Start session until the composer has text", () => {
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    const button = screen.getByText("Start session").closest("button");
    expect(button?.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    expect(button?.disabled).toBe(false);
  });

  it("creates a session with a derived name on submit, then reports it via onSessionCreated with the goal text", async () => {
    const session = makeSession({ id: "s42", name: "Fix the deploy script" });
    const createSession = vi.fn().mockResolvedValue(session);
    const onSessionCreated = vi.fn();
    render(<Launchpad createSession={createSession} onSessionCreated={onSessionCreated} />);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Fix the deploy script", name_source: "derived" }),
      ),
    );
    await waitFor(() =>
      expect(onSessionCreated).toHaveBeenCalledWith(session, "Fix the deploy script"),
    );
  });

  it("fires exactly one create for rapid repeat submits while the request is in flight", async () => {
    // Regression: creating a session takes multi-second server work (agent
    // auto-selection, child spawn) and there was no in-flight guard — every
    // Enter press or click in that window POSTed another real session (the
    // incident: one goal, three sessions, each burning a full turn).
    const session = makeSession({ id: "s43", name: "One goal one session" });
    let resolveCreate!: (s: Session) => void;
    const createSession = vi
      .fn()
      .mockImplementation(() => new Promise<Session>((r) => (resolveCreate = r)));
    const onSessionCreated = vi.fn();
    render(<Launchpad createSession={createSession} onSessionCreated={onSessionCreated} />);

    const composer = screen.getByLabelText("Describe your goal");
    fireEvent.change(composer, { target: { value: "One goal one session" } });
    fireEvent.keyDown(composer, { key: "Enter" });
    fireEvent.keyDown(composer, { key: "Enter" });
    fireEvent.keyDown(composer, { key: "Enter" });
    fireEvent.click(screen.getByText("Starting…"));

    expect(createSession).toHaveBeenCalledTimes(1);
    // Visible pending affordance: the button reads "Starting…" and is
    // disabled instead of sitting inert and clickable.
    expect((screen.getByText("Starting…") as HTMLButtonElement).disabled).toBe(true);

    resolveCreate(session);
    await waitFor(() => expect(onSessionCreated).toHaveBeenCalledTimes(1));
    // Guard releases after the flight: a fresh goal can submit again.
    fireEvent.change(composer, { target: { value: "A second, separate goal" } });
    fireEvent.keyDown(composer, { key: "Enter" });
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(2));
  });

  it("starts a session on a full-trust-only provider at Full, and says why", async () => {
    const hermesMatrix = { trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.includes("/sessions/defaults")) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                provider: { value: "hermes", source: "config" },
                model: {},
                agent: {},
                isolation: {},
                // Even if a preview said otherwise, the pill and the payload follow the matrix.
                trust: { value: "limited", source: "config" },
                dir: {},
              }),
              { status: 200 },
            ),
          );
        }
        if (url.endsWith("/providers")) {
          return Promise.resolve(new Response(JSON.stringify([{ name: "hermes", matrix: hermesMatrix }]), { status: 200 }));
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s-hermes" }));
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);

    await waitFor(() =>
      expect(screen.getByTestId("trust-reason").textContent).toBe("Hermes can't enforce approvals; it runs at full trust"),
    );
    expect(screen.getByText("Full access")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Describe your goal"), { target: { value: "Tidy the README" } });
    fireEvent.click(screen.getByText("Start session"));
    await waitFor(() => expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ trust: "full" })));
  });

  it("selecting Auto sends agent: 'auto' plus the composer's opening_prompt", async () => {
    const session = makeSession({ id: "s44", agent: "investigator-bot" });
    const createSession = vi.fn().mockResolvedValue(session);
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);

    fireEvent.click(screen.getByText("Select persona"));
    fireEvent.click(screen.getByText("Auto"));
    expect(screen.getByText("Auto")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Investigate the flaky login test" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({
          agent: "auto",
          opening_prompt: "Investigate the flaky login test",
        }),
      ),
    );
  });

  it("does not send opening_prompt for an explicitly chosen agent", async () => {
    const session = makeSession({ id: "s45" });
    const createSession = vi.fn().mockResolvedValue(session);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.includes("/agents")) {
          return Promise.resolve(
            new Response(
              JSON.stringify([
                {
                  id: "ag1",
                  name: "backend-architect",
                  prompt: "…",
                  created_at: "2026-07-01T00:00:00Z",
                  updated_at: "2026-07-01T00:00:00Z",
                },
              ]),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);

    fireEvent.click(screen.getByText("Select persona"));
    await waitFor(() => expect(screen.getByText("backend-architect")).toBeTruthy());
    fireEvent.click(screen.getByText("backend-architect"));

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    // Redesign §1: Session.Agent is a persona *id* — the UI now stages the
    // id (the record's id "ag1"), not the display name ("backend-architect").
    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ agent: "ag1" }),
      ),
    );
    expect(createSession.mock.calls[0][0].opening_prompt).toBeUndefined();
  });

  it("stores the full first line, uncut, for a long first message", async () => {
    const longLine = "The deploy script fails on the new Ubuntu runners".repeat(3);
    const session = makeSession({ id: "s43", name: longLine });
    const createSession = vi.fn().mockResolvedValue(session);
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: longLine },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ name: longLine })),
    );
    const calledName = createSession.mock.calls[0][0].name as string;
    expect(calledName).not.toContain("…");
    expect(calledName).toBe(longLine);
  });

  it("submits on Enter without Shift, and inserts a newline on Shift+Enter", () => {
    const createSession = vi.fn().mockResolvedValue(makeSession({}));
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);
    const textarea = screen.getByLabelText("Describe your goal");

    fireEvent.change(textarea, { target: { value: "Fix the thing" } });
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true });
    expect(createSession).not.toHaveBeenCalled();

    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(createSession).toHaveBeenCalled();
  });

  it("shows an error toast when session creation fails", async () => {
    const createSession = vi.fn().mockRejectedValue(new Error("boom"));
    render(<Launchpad createSession={createSession} onSessionCreated={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() => expect(screen.getByText("boom")).toBeTruthy());
  });

  it("shows the 'Inherits from <workspace> defaults' line when a workspace default resolved a field", async () => {
    stubFetchWithSessionDefaults({
      provider: { value: "claudecode", source: "config" },
      model: {},
      agent: { value: "backend-architect", source: "workspace" },
      isolation: {},
      trust: { value: "limited", source: "config" },
      dir: {},
      workspace: "mads",
    });
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);

    await waitFor(() => expect(screen.getByText("Inherits from mads defaults")).toBeTruthy());
  });

  it("does not show the inherit line when the defaults fetch fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.includes("/sessions/defaults")) {
          return Promise.reject(new Error("network error"));
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText(/Inherits from/)).toBeNull();
  });

  it("does not show the inherit line when a workspace resolved but none of its Defaults contributed", async () => {
    stubFetchWithSessionDefaults({
      provider: { value: "claudecode", source: "config" },
      model: { value: "claude-sonnet-5", source: "provider_default" },
      agent: {},
      isolation: { value: "worktree", source: "builtin" },
      trust: { value: "limited", source: "config" },
      dir: {},
      workspace: "mads",
    });
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);

    await waitFor(() => expect(screen.getByText(/Sonnet 5|claude-sonnet-5/)).toBeTruthy());
    expect(screen.queryByText(/Inherits from/)).toBeNull();
  });

  describe("file attachments", () => {
    function attachFile(file: File) {
      const input = screen.getByLabelText("Attach files") as HTMLInputElement;
      fireEvent.change(input, { target: { files: [file] } });
    }

    it("uploads an attached file to a draft and shows a removable chip, with no raw path in the textarea", async () => {
      const client = new FakeRouterClient();
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} routerClient={client} />);
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));

      expect(await screen.findByLabelText("Remove notes.txt")).toBeTruthy();
      await waitFor(() => expect(client.uploadedAttachments).toHaveLength(1));
      expect(client.uploadedAttachments[0].owner).toEqual({ draft: expect.stringMatching(/^draft_/) });
      const textarea = screen.getByLabelText("Describe your goal") as HTMLTextAreaElement;
      expect(textarea.value).toBe("");
    });

    it("shows the Router's thumbnail for an image file and a glyph for a non-image file", async () => {
      const client = new FakeRouterClient();
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} routerClient={client} />);
      attachFile(new File([new Uint8Array(10)], "photo.png", { type: "image/png" }));
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));

      const image = (await screen.findByLabelText("Remove photo.png")).parentElement!;
      await waitFor(() =>
        expect(image.querySelector("img")?.getAttribute("src")).toMatch(
          /^fixture:\/\/attachment-drafts\/draft_\w+\/attachments\/att_1\/thumbnail$/,
        ),
      );
      const doc = screen.getByLabelText("Remove notes.txt").parentElement!;
      expect(doc.querySelector("img")).toBeNull();
    });

    it("stages a pasted screenshot and dropped files", async () => {
      const client = new FakeRouterClient();
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} routerClient={client} />);
      const textarea = screen.getByLabelText("Describe your goal");
      fireEvent.paste(textarea, {
        clipboardData: { files: [new File(["x"], "image.png", { type: "image/png" })] },
      });
      fireEvent.drop(textarea, {
        dataTransfer: { files: [new File(["y"], "trace.txt")], types: ["Files"] },
      });
      await waitFor(() => expect(client.uploadedAttachments).toHaveLength(2));
      expect(client.uploadedAttachments[0].attachment.name).toMatch(/^screenshot-.*\.png$/);
      expect(client.uploadedAttachments[1].attachment.name).toBe("trace.txt");
    });

    it("removing a chip deletes the uploaded file", async () => {
      const client = new FakeRouterClient();
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} routerClient={client} />);
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));
      await screen.findByText("5 B");

      fireEvent.click(screen.getByLabelText("Remove notes.txt"));
      expect(screen.queryByText("notes.txt")).toBeNull();
      await waitFor(() => expect(client.deletedAttachments).toEqual(["att_1"]));
    });

    it("starts the session from the draft and hands the files to its composer, leaving the goal text as typed", async () => {
      const client = new FakeRouterClient();
      const session = makeSession({ id: "s99" });
      const createSession = vi.fn().mockResolvedValue(session);
      const onSessionCreated = vi.fn();
      render(
        <Launchpad
          createSession={createSession}
          onSessionCreated={onSessionCreated}
          routerClient={client}
        />,
      );

      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));
      await screen.findByText("5 B");
      fireEvent.change(screen.getByLabelText("Describe your goal"), {
        target: { value: "Fix the deploy script" },
      });
      fireEvent.click(screen.getByText("Start session"));

      await waitFor(() =>
        expect(onSessionCreated).toHaveBeenCalledWith(session, "Fix the deploy script"),
      );
      const draft = (client.uploadedAttachments[0].owner as { draft: string }).draft;
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Fix the deploy script", attachment_draft: draft }),
      );
      const handed = stagedForSession("s99", client);
      expect(handed.owner).toEqual({ session: "s99" });
      expect(handed.readyIds()).toEqual(["att_1"]);
      // The launchpad's own draft starts over.
      expect(screen.queryByLabelText("Remove notes.txt")).toBeNull();
    });

    it("sends no attachment draft when nothing is attached", async () => {
      const session = makeSession({ id: "s100" });
      const createSession = vi.fn().mockResolvedValue(session);
      const onSessionCreated = vi.fn();
      render(<Launchpad createSession={createSession} onSessionCreated={onSessionCreated} />);

      fireEvent.change(screen.getByLabelText("Describe your goal"), {
        target: { value: "Fix the deploy script" },
      });
      fireEvent.click(screen.getByText("Start session"));

      await waitFor(() =>
        expect(onSessionCreated).toHaveBeenCalledWith(session, "Fix the deploy script"),
      );
      expect(createSession.mock.calls[0][0]).not.toHaveProperty("attachment_draft");
    });

    it("holds Start until uploads land", async () => {
      const client = new FakeRouterClient();
      let land: () => void = () => {};
      vi.spyOn(client, "uploadAttachment").mockImplementation(
        (_owner, file, name) =>
          new Promise((resolve) => {
            land = () =>
              resolve({ id: "a1", name, path: `/d/a1/${name}`, size: file.size, thumbnail: false });
          }),
      );
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} routerClient={client} />);
      fireEvent.change(screen.getByLabelText("Describe your goal"), { target: { value: "go" } });
      attachFile(new File(["hello"], "notes.txt"));

      const start = screen.getByText("Start session").closest("button")!;
      await waitFor(() => expect(start.disabled).toBe(true));
      land();
      await waitFor(() => expect(start.disabled).toBe(false));
    });
  });

  describe("linked sessions (flag-gated)", () => {
    // FLAGS is module-global — reset it or a leak fails unrelated tests.
    afterEach(() => {
      FLAGS.linkedSessions = false;
    });

    const pool = [
      makeSession({ id: "s1", name: "payments refactor" }),
      makeSession({ id: "s2", name: "gateway hardening" }),
    ];

    function stageLinkTo(name: string) {
      fireEvent.click(screen.getByLabelText("More start options"));
      fireEvent.click(screen.getByText("Link to session(s)…"));
      fireEvent.click(screen.getByRole("checkbox", { name }));
      fireEvent.click(screen.getByText("Stage 1 link"));
    }

    it("flag off: renders the plain Start button with no arrow", () => {
      render(
        <Launchpad
          createSession={vi.fn()}
          onSessionCreated={vi.fn()}
          sessions={pool}
        />,
      );
      expect(screen.queryByLabelText("More start options")).toBeNull();
      expect(screen.getByText("Start session")).toBeTruthy();
    });

    it("stages links via the split button's dialog, rendering a chip and relabeling to Start & link", () => {
      FLAGS.linkedSessions = true;
      render(
        <Launchpad
          createSession={vi.fn()}
          onSessionCreated={vi.fn()}
          sessions={pool}
        />,
      );
      stageLinkTo("payments refactor");

      expect(screen.getByText("Leader of payments refactor")).toBeTruthy();
      expect(screen.getByText("Start & link")).toBeTruthy();
      expect(screen.queryByText("Start session")).toBeNull();
    });

    it("threads staged links into the create payload and clears the chips on success", async () => {
      FLAGS.linkedSessions = true;
      const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s9" }));
      render(
        <Launchpad
          createSession={createSession}
          onSessionCreated={vi.fn()}
          sessions={pool}
        />,
      );
      stageLinkTo("payments refactor");
      fireEvent.change(screen.getByLabelText("Describe your goal"), {
        target: { value: "Coordinate the refactor" },
      });
      fireEvent.click(screen.getByText("Start & link"));

      await waitFor(() =>
        expect(createSession).toHaveBeenCalledWith(
          expect.objectContaining({
            links: [
              {
                granting_session_id: "$new",
                subject_session_id: "s1",
                preset: "leader",
                cadence: "on_signal",
              },
            ],
          }),
        ),
      );
      await waitFor(() => expect(screen.queryByText("Leader of payments refactor")).toBeNull());
      expect(screen.getByText("Start session")).toBeTruthy();
    });

    it("removing every staged chip reverts the button and drops links from the payload", async () => {
      FLAGS.linkedSessions = true;
      const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s9" }));
      render(
        <Launchpad
          createSession={createSession}
          onSessionCreated={vi.fn()}
          sessions={pool}
        />,
      );
      stageLinkTo("payments refactor");
      fireEvent.click(screen.getByLabelText("Remove link to payments refactor"));

      expect(screen.getByText("Start session")).toBeTruthy();
      fireEvent.change(screen.getByLabelText("Describe your goal"), {
        target: { value: "Coordinate the refactor" },
      });
      fireEvent.click(screen.getByText("Start session"));

      await waitFor(() => expect(createSession).toHaveBeenCalled());
      expect(createSession.mock.calls[0][0].links).toBeUndefined();
    });

    it("Enter-to-submit includes the staged links (keyboard path shares submit)", async () => {
      FLAGS.linkedSessions = true;
      const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s9" }));
      render(
        <Launchpad
          createSession={createSession}
          onSessionCreated={vi.fn()}
          sessions={pool}
        />,
      );
      stageLinkTo("gateway hardening");
      const textarea = screen.getByLabelText("Describe your goal");
      fireEvent.change(textarea, { target: { value: "Coordinate the refactor" } });
      fireEvent.keyDown(textarea, { key: "Enter" });

      await waitFor(() =>
        expect(createSession).toHaveBeenCalledWith(
          expect.objectContaining({
            links: [
              {
                granting_session_id: "$new",
                subject_session_id: "s2",
                preset: "leader",
                cadence: "on_signal",
              },
            ],
          }),
        ),
      );
    });
  });
});
