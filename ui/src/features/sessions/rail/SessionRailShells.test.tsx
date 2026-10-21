import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { makeFakeShellHost } from "../../../conversation/ShellDock/fakeShellHost";
import {
  resetShellDockCache,
  setShellDockState,
} from "../../../conversation/ShellDock/dockState";
import type { ShellInfo } from "../../../conversation/TerminalRenderer/ptyBridge";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const shellFor = (sessionId: string): ShellInfo => ({
  sessionId,
  shellId: `shell-${sessionId}`,
  cwd: "/tmp",
  pid: 1,
  startedAt: 0,
  attached: false,
});

let fake: ReturnType<typeof makeFakeShellHost>;

beforeEach(() => {
  window.localStorage.clear();
  resetShellDockCache();
  fake = makeFakeShellHost();
  (window as unknown as { mads: unknown }).mads = { shell: fake.host };
});

afterEach(() => {
  delete (window as unknown as { mads?: unknown }).mads;
  resetShellDockCache();
});

function card(name: string) {
  return screen.getByText(name).closest('[role="button"]') as HTMLElement;
}

function renderRail(selectedId?: string) {
  return render(
    <SessionRail
      sessions={[makeSession({ id: "s1", name: "has shell" }), makeSession({ id: "s2", name: "plain" })]}
      openTaskCounts={{}}
      selectedId={selectedId}
    />,
  );
}

describe("SessionRail shell cards", () => {
  it("styles a card whose session has a live shell, and reverts when it ends", async () => {
    fake.setShells([shellFor("s1")]);
    renderRail();
    await waitFor(() => expect(card("has shell").querySelector("[data-shell-prompt]")).not.toBeNull());

    const shellCard = card("has shell");
    const prompt = shellCard.querySelector("[data-shell-prompt]")!;
    expect(prompt.textContent).toBe(">_");
    expect(prompt.getAttribute("aria-hidden")).toBe("true");
    const status = shellCard.querySelector('[role="status"]')!;
    // Same tone as the status line, by sharing its class.
    const tone = /status-\w+/.exec(status.className)![0];
    expect(prompt.className).toContain(tone);
    expect(shellCard.className).toMatch(/shellCard/);
    expect(card("plain").querySelector("[data-shell-prompt]")).toBeNull();
    expect(card("plain").className).not.toMatch(/shellCard/);

    act(() => fake.setShells([]));
    await waitFor(() => expect(card("has shell").querySelector("[data-shell-prompt]")).toBeNull());
    expect(card("has shell").className).not.toMatch(/shellCard/);
  });

  // A shell found already running (the app opening, a card mounting) just
  // shows; one that starts or ends while the card is on screen animates.
  it("shows an already-running shell still, and types in one that just started", async () => {
    const running = { ...shellFor("s1"), startedAt: Date.now() - 60_000 };
    fake.setShells([running]);
    renderRail();
    await waitFor(() => expect(card("has shell").querySelector("[data-shell-prompt]")).not.toBeNull());
    expect(card("has shell").querySelector("[data-typing]")).toBeNull();
    expect(card("has shell").dataset.shellMotion).toBeUndefined();

    act(() => fake.setShells([running, { ...shellFor("s2"), startedAt: Date.now() }]));
    await waitFor(() => expect(card("plain").querySelector("[data-shell-prompt]")).not.toBeNull());
    expect(card("plain").querySelector("[data-typing]")).not.toBeNull();
    expect(card("plain").dataset.shellMotion).toBe("true");
  });

  it("erases the prompt before closing its slot when a shell ends", async () => {
    fake.setShells([shellFor("s1")]);
    renderRail();
    await waitFor(() => expect(card("has shell").querySelector("[data-shell-prompt]")).not.toBeNull());

    act(() => fake.setShells([]));
    await waitFor(() => expect(card("has shell").querySelector("[data-erasing]")).not.toBeNull());
    // Still a shell card while it erases: the face and indent go with the slot.
    expect(card("has shell").className).toMatch(/shellCard/);
    await waitFor(() => expect(card("has shell").querySelector("[data-shell-prompt]")).toBeNull());
    expect(card("has shell").className).not.toMatch(/shellCard/);
  });

  it("subscribes to the shell set once for the whole rail", async () => {
    fake.setShells([shellFor("s1")]);
    renderRail();
    await waitFor(() => expect(fake.host.list).toHaveBeenCalled());
    expect(fake.host.onShellsChanged).toHaveBeenCalledTimes(1);
    expect(fake.host.list).toHaveBeenCalledTimes(1);
  });

  it("says Shell open when the dock is visible and Shell hidden when it is not", async () => {
    fake.setShells([shellFor("s1")]);
    renderRail("s1");
    await waitFor(() => expect(card("has shell").title).toContain("Shell hidden"));
    expect(card("plain").title).not.toContain("Shell");
    act(() => setShellDockState("s1", { open: true }));
    expect(card("has shell").title).toContain("Shell open");
    expect(card("has shell").title).not.toContain("Shell hidden");
  });

  it("says Shell hidden for a session that isn't on screen, even if its dock was left open", async () => {
    fake.setShells([shellFor("s1")]);
    act(() => setShellDockState("s1", { open: true }));
    renderRail("s2");
    await waitFor(() => expect(card("has shell").title).toContain("Shell hidden"));
  });

  it("inspects a hidden shell once per hover, never on render, and shows what runs", async () => {
    fake.host.inspect.mockResolvedValue([
      {
        sessionId: "s1",
        shellId: "shell-s1",
        busy: true,
        foreground: "python3",
        processes: [
          { pid: 2, command: "sudo python3 -m http.server 8099" },
          { pid: 3, command: "tail -f log" },
          { pid: 4, command: "sleep 9" },
        ],
        count: 3,
        rssKb: 1,
      },
    ]);
    fake.setShells([shellFor("s1")]);
    renderRail();
    await waitFor(() => expect(card("has shell").title).toContain("Shell hidden"));
    expect(fake.host.inspect).not.toHaveBeenCalled();

    fireEvent.mouseEnter(card("has shell"));
    await waitFor(() =>
      expect(card("has shell").title).toContain(
        "Shell hidden · running: sudo python3 -m http.server 8099 (+2 more)",
      ),
    );
    expect(fake.host.inspect).toHaveBeenCalledTimes(1);
    expect(fake.host.inspect).toHaveBeenCalledWith("shell-s1");

    fireEvent.mouseEnter(card("plain"));
    expect(fake.host.inspect).toHaveBeenCalledTimes(1);
  });

  it("does not inspect a visible shell, and swallows an inspect failure", async () => {
    fake.setShells([shellFor("s1")]);
    act(() => setShellDockState("s1", { open: true }));
    renderRail("s1");
    await waitFor(() => expect(card("has shell").title).toContain("Shell open"));
    fireEvent.mouseEnter(card("has shell"));
    expect(fake.host.inspect).not.toHaveBeenCalled();

    act(() => setShellDockState("s1", { open: false }));
    fake.host.inspect.mockRejectedValue(new Error("host gone"));
    fireEvent.mouseEnter(card("has shell"));
    await waitFor(() => expect(fake.host.inspect).toHaveBeenCalledTimes(1));
    expect(card("has shell").title).toContain("Shell hidden");
    expect(card("has shell").title).not.toContain("running");
  });

  it("changes nothing outside Electron", () => {
    delete (window as unknown as { mads?: unknown }).mads;
    renderRail();
    expect(document.querySelector("[data-shell-prompt]")).toBeNull();
    fireEvent.mouseEnter(card("has shell"));
    expect(card("has shell").title).not.toContain("Shell");
  });
});
