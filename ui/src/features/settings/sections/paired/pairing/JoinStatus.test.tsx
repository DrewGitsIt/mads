import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { resetDevicesForTests, type JoinStatus as Status } from "../../../../../router/devices";
import { JOIN_POLL_MAX_MS, JOIN_POLL_MS, JoinStatus } from "./JoinStatus";
import { fp, stubRouter } from "../testRouter";

afterEach(() => {
  resetDevicesForTests();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const pending = (expiresInMs: number): Status => ({
  id: "pr_1",
  state: "pending",
  host: { name: "spark", fingerprint: fp("K") },
  expires_at: new Date(Date.now() + expiresInMs).toISOString(),
});

const advance = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

describe("JoinStatus polling", () => {
  it("keeps polling through failed polls, backing off, and lands the host's answer", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let fail = true;
    const calls = stubRouter({
      "GET /devices/pair/pr_1": () =>
        fail ? new Response(JSON.stringify({ error: "the Router is restarting" }), { status: 502 }) : { ...pending(300_000), state: "declined" },
    });
    const polls = () => calls.filter((c) => c.path === "/devices/pair/pr_1").length;
    render(<JoinStatus initial={pending(300_000)} selfName="x" host="MacBook-Air" rows={[]} onClose={() => {}} />);
    await advance(JOIN_POLL_MS + 50);
    expect(polls()).toBe(1);
    expect(await screen.findByRole("alert")).toBeTruthy();
    // The next poll waits twice as long.
    await advance(JOIN_POLL_MS + 50);
    expect(polls()).toBe(1);
    await advance(JOIN_POLL_MS);
    expect(polls()).toBe(2);
    // Never longer than the cap.
    for (let i = 0; i < 6; i++) await advance(JOIN_POLL_MAX_MS);
    const before = polls();
    await advance(JOIN_POLL_MAX_MS + 50);
    expect(polls()).toBe(before + 1);
    fail = false;
    await advance(JOIN_POLL_MAX_MS + 50);
    expect(await screen.findByRole("dialog", { name: "spark declined" })).toBeTruthy();
  });

  it("stops at the code's expiry when polls keep failing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({
      "GET /devices/pair/pr_1": () => new Response(JSON.stringify({ error: "down" }), { status: 502 }),
    });
    render(<JoinStatus initial={pending(5_000)} selfName="x" host="x" rows={[]} onClose={() => {}} />);
    await advance(JOIN_POLL_MS + 50);
    await advance(2 * JOIN_POLL_MS + 50);
    await advance(4 * JOIN_POLL_MS + 50);
    expect(await screen.findByRole("dialog", { name: "spark didn't answer" })).toBeTruthy();
    const n = calls.length;
    await advance(JOIN_POLL_MAX_MS * 2);
    expect(calls.length).toBe(n);
  });
});

describe("JoinStatus, a found device's number (S1)", () => {
  it("Did it show this number?: Yes waits a moment, No has the focus", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/pair/pr_1/confirm": { ...pending(300_000), state: "approved", confirm: "matched", level: "view" }, "GET /devices": [] });
    render(
      <JoinStatus
        initial={{ ...pending(300_000), state: "approved", level: "view", mode: "discovered", check_code: "482913", confirm: "needed" }}
        selfName="x"
        host="MacBook-Air"
        rows={[]}
        onClose={() => {}}
      />,
    );
    const yes = screen.getByRole("button", { name: "Yes, the same number" }) as HTMLButtonElement;
    expect(yes.disabled).toBe(true);
    expect(document.activeElement?.textContent).toBe("No, it was different");
    act(() => yes.click());
    expect(calls.some((c) => c.path.endsWith("/confirm"))).toBe(false);
    await advance(1000);
    expect(yes.disabled).toBe(false);
  });
});

