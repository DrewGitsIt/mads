import { useState } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ShellsSection } from "./ShellsSection";
import { makeFakeShellHost } from "../../../conversation/ShellDock/fakeShellHost";
import { resetShellDockCache, setShellDockState } from "../../../conversation/ShellDock/dockState";
import { OPEN_SESSION_EVENT } from "../../../appNavigation";
import type { ShellActivity, ShellInfo } from "../../../conversation/TerminalRenderer/ptyBridge";

// The sessions the app already has come from useSessionsList (a router fetch
// plus SSE); the section only needs ids and names.
const sessionsMock = vi.hoisted(() => ({ sessions: [] as Array<{ id: string; name: string }> }));
vi.mock("../../sessions/state/useSessionsList", () => ({
  useSessionsList: () => ({ sessions: sessionsMock.sessions, loading: false, error: null }),
}));

const shell = (n: number, over: Partial<ShellInfo> = {}): ShellInfo => ({
  sessionId: `ses_${n}`,
  shellId: `shell-ses_${n}`,
  cwd: `/home/drew/repo/p${n}`,
  pid: 100 + n,
  startedAt: Date.now() - 5 * 60_000,
  attached: false,
  ...over,
});

const activity = (n: number, commands: string[], rssKb = 2048): ShellActivity => ({
  sessionId: `ses_${n}`,
  shellId: `shell-ses_${n}`,
  busy: commands.length > 0,
  foreground: undefined,
  processes: commands.map((command, i) => ({ pid: 900 + i, command })),
  count: commands.length,
  rssKb,
});

/** IntersectionObserver whose visibility the test drives. */
let setVisible: (visible: boolean) => void = () => {};
class FakeIntersectionObserver {
  constructor(private cb: (entries: Array<{ isIntersecting: boolean }>) => void) {
    setVisible = (v) => this.cb([{ isIntersecting: v }]);
  }
  observe() {}
  disconnect() {}
  unobserve() {}
}

function setup(shells: ShellInfo[], acts: ShellActivity[]) {
  const fake = makeFakeShellHost();
  fake.setShells(shells);
  fake.host.inspect.mockImplementation(async (id?: string) =>
    acts.filter((a) => id === undefined || a.shellId === id),
  );
  window.mads = { shell: fake.host };
  return fake;
}

/** The Shells row with its sheet state held as SettingsSurface holds it; open unless `closed`. */
function Harness({ closed = false }: { closed?: boolean }) {
  const [open, setOpen] = useState(!closed);
  return <ShellsSection open={open} onOpenChange={setOpen} />;
}

/** Mounts the section and scrolls it on screen, as inspect only runs while it shows. */
async function renderVisible() {
  const utils = render(<Harness />);
  await act(async () => setVisible(true));
  return utils;
}

describe("ShellsSection", () => {
  beforeEach(() => {
    sessionsMock.sessions = [
      { id: "ses_1", name: "Fix the rail" },
      { id: "ses_2", name: "Docs" },
    ];
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    window.localStorage.clear();
    resetShellDockCache();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    delete (window as { mads?: unknown }).mads;
  });

  it("outside Electron says it needs the desktop app", () => {
    render(<Harness closed />);
    expect(screen.getByTestId("shells-summary").textContent).toBe("Desktop app only");
    fireEvent.click(screen.getByRole("button", { name: "Manage…" }));
    expect(
      within(screen.getByRole("complementary", { name: "Shells" })).getByText("Shells need the desktop app."),
    ).toBeTruthy();
  });

  describe("summary row", () => {
    it("counts running and idle shells while on screen, without opening the inventory", async () => {
      setup([shell(1), shell(2), shell(3)], [activity(1, []), activity(2, ["make watch"]), activity(3, [])]);
      render(<Harness closed />);
      // Off screen nothing is inspected, so no shell is known to be idle yet.
      await waitFor(() => expect(screen.getByTestId("shells-summary").textContent).toBe("3 running"));
      await act(async () => setVisible(true));
      await waitFor(() => expect(screen.getByTestId("shells-summary").textContent).toBe("1 running · 2 idle"));
      expect(screen.queryByRole("complementary")).toBeNull();
    });

    it("says None running with no shells", () => {
      setup([], []);
      render(<Harness closed />);
      expect(screen.getByTestId("shells-summary").textContent).toBe("None running");
    });

    it("Manage… opens the inventory in a sheet; ✕ and Escape close it", async () => {
      setup([shell(1)], [activity(1, [])]);
      render(<Harness closed />);
      fireEvent.click(screen.getByRole("button", { name: "Manage…" }));
      const sheet = screen.getByRole("complementary", { name: "Shells" });
      expect(await within(sheet).findByRole("button", { name: "End" })).toBeTruthy();

      fireEvent.click(within(sheet).getByRole("button", { name: "Close" }));
      expect(screen.queryByRole("complementary")).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Manage…" }));
      fireEvent.keyDown(document, { key: "Escape" });
      expect(screen.queryByRole("complementary")).toBeNull();
    });

    it("polls while the sheet is open even with the row off screen", async () => {
      vi.useFakeTimers();
      const fake = setup([shell(1)], [activity(1, [])]);
      render(<Harness />);
      await act(async () => {});
      expect(fake.host.inspect).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fake.host.inspect).toHaveBeenCalledTimes(2);

      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fake.host.inspect).toHaveBeenCalledTimes(2);
    });
  });

  it("with no shells says so, and never asks the host", async () => {
    const fake = setup([], []);
    render(<Harness />);
    expect(await screen.findByText("No shells are running.")).toBeTruthy();
    await act(async () => {});
    expect(fake.host.inspect).not.toHaveBeenCalled();
  });

  it("renders a row per shell: session link or 'session gone', folder, started, dock, running, memory", async () => {
    setShellDockState("ses_1", { open: true });
    setup(
      [shell(1), shell(3)],
      [activity(1, ["tail -f log", "sleep 9", "vim"], 3072), activity(3, [], 1024)],
    );
    await renderVisible();

    const rows = await screen.findAllByRole("row");
    const first = within(rows[1]);
    expect(first.getByRole("button", { name: "Fix the rail" })).toBeTruthy();
    expect(first.getByText("~/repo/p1")).toBeTruthy();
    expect(first.getByText("5m")).toBeTruthy();
    expect(first.getByText("open")).toBeTruthy();
    expect(await first.findByText("tail -f log (+2 more)")).toBeTruthy();
    expect(first.getByText("3 MB")).toBeTruthy();

    const second = within(rows[2]);
    expect(second.getByText("session gone")).toBeTruthy();
    expect(second.getByText("hidden")).toBeTruthy();
    expect(await second.findByText("idle")).toBeTruthy();
  });

  it("the session link opens that session", async () => {
    setup([shell(2)], [activity(2, [])]);
    render(<Harness />);
    const opened = vi.fn();
    window.addEventListener(OPEN_SESSION_EVENT, opened);
    fireEvent.click(await screen.findByRole("button", { name: "Docs" }));
    window.removeEventListener(OPEN_SESSION_EVENT, opened);
    expect((opened.mock.calls[0][0] as CustomEvent).detail).toEqual({ sessionId: "ses_2" });
  });

  describe("inspecting", () => {
    it("asks on becoming visible, then every 5 s, and stops when hidden", async () => {
      vi.useFakeTimers();
      const fake = setup([shell(1)], [activity(1, [])]);
      render(<Harness closed />);
      await act(async () => {});
      expect(fake.host.inspect).not.toHaveBeenCalled(); // off screen

      await act(async () => setVisible(true));
      expect(fake.host.inspect).toHaveBeenCalledTimes(1);
      expect(fake.host.inspect).toHaveBeenLastCalledWith();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fake.host.inspect).toHaveBeenCalledTimes(2);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(fake.host.inspect).toHaveBeenCalledTimes(4);

      await act(async () => setVisible(false));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fake.host.inspect).toHaveBeenCalledTimes(4);
    });

    it("stops when unmounted", async () => {
      vi.useFakeTimers();
      const fake = setup([shell(1)], [activity(1, [])]);
      const { unmount } = render(<Harness closed />);
      await act(async () => setVisible(true));
      expect(fake.host.inspect).toHaveBeenCalledTimes(1);
      unmount();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(fake.host.inspect).toHaveBeenCalledTimes(1);
    });

    it("never asks while no shell is live, and starts when one appears", async () => {
      vi.useFakeTimers();
      const fake = setup([], []);
      render(<Harness closed />);
      await act(async () => setVisible(true));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(fake.host.inspect).not.toHaveBeenCalled();

      await act(async () => fake.setShells([shell(1)]));
      expect(fake.host.inspect).toHaveBeenCalledTimes(1);
    });
  });

  describe("End", () => {
    it("ends an idle shell at once", async () => {
      const fake = setup([shell(1)], [activity(1, [])]);
      await renderVisible();
      fireEvent.click(await screen.findByRole("button", { name: "End" }));
      await waitFor(() => expect(fake.host.end).toHaveBeenCalledWith("shell-ses_1"));
      expect(screen.queryByRole("dialog")).toBeNull();
    });

    it("asks first for a busy shell: Keep running leaves it, End shell ends it", async () => {
      const fake = setup([shell(1)], [activity(1, ["tail -f log"])]);
      await renderVisible();
      fireEvent.click(await screen.findByRole("button", { name: "End" }));

      expect(await screen.findByText("A process is still running in this shell")).toBeTruthy();
      expect(screen.getByText(/stops it/)).toBeTruthy();
      expect(fake.host.end).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole("button", { name: "Keep running" }));
      expect(screen.queryByText("A process is still running in this shell")).toBeNull();
      expect(fake.host.end).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole("button", { name: "End" }));
      fireEvent.click(await screen.findByRole("button", { name: "End shell" }));
      await waitFor(() => expect(fake.host.end).toHaveBeenCalledWith("shell-ses_1"));
    });

    it("the list follows the host's push after an end, with no refresh", async () => {
      const fake = setup([shell(1)], [activity(1, [])]);
      render(<Harness />);
      await screen.findByRole("button", { name: "End" });
      act(() => fake.setShells([]));
      expect(await screen.findByText("No shells are running.")).toBeTruthy();
    });
  });

  describe("End all idle", () => {
    it("ends the idle shells and skips the busy one", async () => {
      const fake = setup(
        [shell(1), shell(2), shell(3)],
        [activity(1, []), activity(2, ["make watch"]), activity(3, [])],
      );
      await renderVisible();
      const button = await screen.findByRole("button", { name: "End all idle" });
      await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
      fireEvent.click(button);

      await waitFor(() => expect(fake.host.end).toHaveBeenCalledTimes(2));
      expect(fake.host.end).toHaveBeenCalledWith("shell-ses_1");
      expect(fake.host.end).toHaveBeenCalledWith("shell-ses_3");
      expect(fake.host.end).not.toHaveBeenCalledWith("shell-ses_2");
    });

    it("is disabled when nothing is idle", async () => {
      setup([shell(2)], [activity(2, ["make watch"])]);
      await renderVisible();
      const button = (await screen.findByRole("button", { name: "End all idle" })) as HTMLButtonElement;
      await screen.findByText("make watch");
      expect(button.disabled).toBe(true);
    });
  });
});
