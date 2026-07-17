import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Launchpad, deriveSessionName } from "./Launchpad";
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
  it("uses the first line, truncated", () => {
    expect(deriveSessionName("Fix the deploy script\nmore detail")).toBe("Fix the deploy script");
    expect(deriveSessionName("x".repeat(80))).toBe(`${"x".repeat(60)}…`);
  });
});

describe("Launchpad", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    stubEmptyFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("disables Start session until the composer has text", () => {
    render(<Launchpad createSession={vi.fn()} />);
    const button = screen.getByText("Start session").closest("button");
    expect(button?.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    expect(button?.disabled).toBe(false);
  });

  it("creates a session with a derived name on submit, then shows its provisioning strip", async () => {
    const createSession = vi.fn().mockResolvedValue(makeSession({ id: "s42", name: "Fix the deploy script" }));
    render(<Launchpad createSession={createSession} />);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() =>
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Fix the deploy script" }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("status", { name: "Session provisioning" })).toBeTruthy(),
    );
  });

  it("submits on Enter without Shift, and inserts a newline on Shift+Enter", () => {
    const createSession = vi.fn().mockResolvedValue(makeSession({}));
    render(<Launchpad createSession={createSession} />);
    const textarea = screen.getByLabelText("Describe your goal");

    fireEvent.change(textarea, { target: { value: "Fix the thing" } });
    fireEvent.keyDown(textarea, { key: "Enter", shiftKey: true });
    expect(createSession).not.toHaveBeenCalled();

    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(createSession).toHaveBeenCalled();
  });

  it("shows an error toast when session creation fails", async () => {
    const createSession = vi.fn().mockRejectedValue(new Error("boom"));
    render(<Launchpad createSession={createSession} />);

    fireEvent.change(screen.getByLabelText("Describe your goal"), {
      target: { value: "Fix the deploy script" },
    });
    fireEvent.click(screen.getByText("Start session"));

    await waitFor(() => expect(screen.getByText("boom")).toBeTruthy());
  });

  it("fills the composer when a suggestion chip is picked, without creating a session", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL) => {
        const url = input.toString();
        if (url.includes("/sessions") && !url.includes("/tasks")) {
          return Promise.resolve(
            new Response(JSON.stringify([makeSession({ id: "s1", name: "Merge-back UX" })]), {
              status: 200,
            }),
          );
        }
        if (url.includes("/tasks")) {
          return Promise.resolve(
            new Response(
              JSON.stringify([
                {
                  id: "t1",
                  session_id: "s1",
                  status: "ready",
                  retry_count: 0,
                  created_at: "2026-07-01T00:00:00Z",
                  updated_at: "2026-07-01T00:00:00Z",
                  title: "Fix merge conflict UI",
                },
              ]),
              { status: 200 },
            ),
          );
        }
        return Promise.resolve(new Response("[]", { status: 200 }));
      }),
    );
    const createSession = vi.fn();
    render(<Launchpad createSession={createSession} />);

    const chip = await screen.findByText(/Resume: Merge-back UX/);
    fireEvent.click(chip);

    const textarea = screen.getByLabelText("Describe your goal") as HTMLTextAreaElement;
    expect(textarea.value).toBe("Resume: Merge-back UX — 1 open task");
    expect(createSession).not.toHaveBeenCalled();
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
    render(<Launchpad createSession={vi.fn()} />);

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
    render(<Launchpad createSession={vi.fn()} />);

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
    render(<Launchpad createSession={vi.fn()} />);

    await waitFor(() => expect(screen.getByText(/Sonnet 5|claude-sonnet-5/)).toBeTruthy());
    expect(screen.queryByText(/Inherits from/)).toBeNull();
  });
});
