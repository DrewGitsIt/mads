import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Launchpad, deriveSessionName } from "./Launchpad";
import { FLAGS } from "../../../featureFlags";
import { FakeEventSource } from "../../../router/testFakeEventSource";
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

    it("renders a removable chip for an attached file, with no raw path in the textarea", () => {
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));

      expect(screen.getByText("notes.txt")).toBeTruthy();
      const textarea = screen.getByLabelText("Describe your goal") as HTMLTextAreaElement;
      expect(textarea.value).toBe("");
      expect(textarea.value).not.toContain("notes.txt");
    });

    it("shows a thumbnail for an image file and a glyph for a non-image file", () => {
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
      attachFile(new File([new Uint8Array(10)], "photo.png", { type: "image/png" }));
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));

      const image = screen.getByText("photo.png").closest("span")?.parentElement;
      expect(image?.querySelector("img")).toBeTruthy();
      const doc = screen.getByText("notes.txt").closest("span")?.parentElement;
      expect(doc?.querySelector("img")).toBeNull();
    });

    it("removing a chip drops the file", () => {
      render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));
      expect(screen.getByText("notes.txt")).toBeTruthy();

      fireEvent.click(screen.getByLabelText("Remove notes.txt"));
      expect(screen.queryByText("notes.txt")).toBeNull();
    });

    it("prepends an injected reference preamble the user did not type to the goal text sent on start", async () => {
      const session = makeSession({ id: "s99" });
      const createSession = vi.fn().mockResolvedValue(session);
      const onSessionCreated = vi.fn();
      render(<Launchpad createSession={createSession} onSessionCreated={onSessionCreated} />);

      attachFile(new File(["hello"], "notes.txt", { type: "text/plain" }));
      fireEvent.change(screen.getByLabelText("Describe your goal"), {
        target: { value: "Fix the deploy script" },
      });
      fireEvent.click(screen.getByText("Start session"));

      await waitFor(() => expect(onSessionCreated).toHaveBeenCalled());
      const goalText = onSessionCreated.mock.calls[0][1] as string;
      expect(goalText).toContain("See these files for reference: notes.txt");
      expect(goalText).toContain("Fix the deploy script");
      // The session's own name is still derived from what the user typed, not the preamble.
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Fix the deploy script" }),
      );
    });

    it("does not inject a preamble when no files are attached", async () => {
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
              { granting_session_id: "$new", subject_session_id: "s1", preset: "leader" },
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
              { granting_session_id: "$new", subject_session_id: "s2", preset: "leader" },
            ],
          }),
        ),
      );
    });
  });
});
