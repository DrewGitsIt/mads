// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { HostRequests } from "./hostRequests";

function make() {
  const posted: Array<{ type: string; requestId: number }> = [];
  const timers: Array<{ fn: () => void; ms: number; live: boolean }> = [];
  const requests = new HostRequests(
    (m) => posted.push(m),
    (fn, ms) => {
      const t = { fn, ms, live: true };
      timers.push(t);
      return () => {
        t.live = false;
      };
    },
  );
  return { requests, posted, timers };
}

describe("HostRequests", () => {
  it("resolves with the host's answer for the matching id and cancels the timeout", async () => {
    const { requests, posted, timers } = make();
    const a = requests.request("inspect", 1500);
    const b = requests.request("end-all", 1000);
    expect(posted).toEqual([
      { type: "inspect", requestId: 1 },
      { type: "end-all", requestId: 2 },
    ]);
    requests.reply(2, "done");
    requests.reply(1, ["x"]);
    expect(await b).toBe("done");
    expect(await a).toEqual(["x"]);
    expect(timers.every((t) => !t.live)).toBe(true);
  });

  it("resolves undefined when the timeout fires, and ignores a late answer", async () => {
    const { requests, timers } = make();
    const pending = requests.request("end-all", 1000);
    expect(timers[0].ms).toBe(1000);
    timers[0].fn();
    expect(await pending).toBeUndefined();
    expect(() => requests.reply(1, "late")).not.toThrow();
  });

  it("failAll settles everything with undefined", async () => {
    const { requests } = make();
    const spy = vi.fn();
    const p = requests.request("inspect", 1500).then(spy);
    requests.failAll();
    await p;
    expect(spy).toHaveBeenCalledWith(undefined);
  });
});
