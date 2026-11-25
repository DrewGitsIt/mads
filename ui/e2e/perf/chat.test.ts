import { describe, expect, it } from "vitest";
import { alnum, analyzeTrace, expectedLoad, faultedAppend, proxyDeltaTimes, replyMatches, summarize, traceBucket, type TraceEvent } from "./chat";
import type { ProxySnapshot } from "./proxyClient";

const deltas = ["## Merge the tail\n\n", "Every row keeps its `key`. ", "**Short** note on ", "the stream.\n\n```\nx := y(z, 42)\n```\n"];
const wire = alnum(deltas.join(""));

/** The probe's reference as faultedAppend builds it, delta by delta. */
function reference(fault: string, at: number): string {
  let ref = "";
  let held = "";
  deltas.forEach((d, i) => ([ref, held] = faultedAppend(ref, held, i, alnum(d), fault, at)));
  return ref + held;
}

describe("the streamed text check", () => {
  it("passes the reply as streamed, whole and as any prefix", () => {
    expect(replyMatches(wire, wire, true)).toBe(true);
    for (let n = 0; n <= wire.length; n++) expect(replyMatches(wire.slice(0, n), wire, false)).toBe(true);
  });

  it("fails a dropped, doubled or reordered delta (whole and mid-stream)", () => {
    const parts = deltas.map(alnum);
    const dropped = parts[0] + parts[2] + parts[3];
    const doubled = parts[0] + parts[1] + parts[1] + parts[2] + parts[3];
    const swapped = parts[0] + parts[2] + parts[1] + parts[3];
    for (const bad of [dropped, doubled, swapped]) {
      expect(replyMatches(bad, wire, true)).toBe(false);
      // Mid-stream: the frame after the fault already shows it.
      expect(replyMatches(bad.slice(0, parts[0].length + parts[1].length + 3), wire, false)).toBe(false);
    }
  });

  it("fails a correct render against a faulted reference (the probe's PERF_CHAT_FAULT self-test)", () => {
    expect(reference("", 1)).toBe(wire);
    for (const f of ["drop", "dup", "swap"]) {
      const ref = reference(f, 1);
      expect(ref).not.toBe(wire);
      expect(replyMatches(wire, ref, true)).toBe(false);
    }
  });

  it("tolerates a message stamp read with the reply at either end, in any of formatMessageTime's forms", () => {
    for (const stamp of ["22:59", "Sep 28 16:54", "10:59 PM", "Nov 3 9:05 AM"]) {
      expect(replyMatches(alnum(stamp) + wire, wire, true)).toBe(true);
      expect(replyMatches(wire + alnum(stamp), wire, true)).toBe(true);
      expect(replyMatches(wire.slice(0, 20) + alnum(stamp), wire, false)).toBe(true);
    }
    // But not arbitrary text.
    expect(replyMatches(wire + "Merge", wire, true)).toBe(false);
  });

  it("survives being injected into the page as source", () => {
    const f = new Function(`return ${replyMatches.toString()}`)() as typeof replyMatches;
    const g = new Function(`return ${faultedAppend.toString()}`)() as typeof faultedAppend;
    expect(f(wire, wire, true)).toBe(true);
    expect(f(wire.slice(1), wire, true)).toBe(false);
    expect(g("ab", "", 3, "cd", "drop", 3)).toEqual(["ab", ""]);
  });
});

describe("expectedLoad", () => {
  // 350 rows served 100 at a time from the end, as transcriptpage pages them.
  const all = Array.from({ length: 350 }, (_, i) => ({ role: i % 7 === 0 ? "user" : "agent", key: `k${i}`, notice: i === 343 }));
  const get = async (before?: string) => {
    const end = before ? all.findIndex((r) => r.key === before) : all.length;
    const from = Math.max(0, end - 100);
    return { rows: all.slice(from, end), has_more: from > 0 };
  };
  it("pages as the app does: older pages, rows and user bubbles for each size", async () => {
    expect(await expectedLoad(get, 1)).toEqual({ older: 0, rows: 100, users: all.slice(250).filter((r) => r.role === "user" && !r.notice).length });
    expect(await expectedLoad(get, 2)).toMatchObject({ older: 1, rows: 200 });
    expect(await expectedLoad(get, 10)).toMatchObject({ older: 3, rows: 350 });
    expect(await expectedLoad(get, "all")).toMatchObject({ older: 3, rows: 350, users: 49 });
  });
});

describe("summarize", () => {
  // 10 deltas 100 ms apart, each 10 letters; a frame every 16 ms; each frame renders what came 20 ms before it.
  const ds: [number, number][] = Array.from({ length: 10 }, (_, i) => [1000 + i * 100, (i + 1) * 10]);
  const frames = Array.from({ length: 70 }, (_, i) => 1000 + i * 16);
  const samples = frames.map((t) => {
    const n = ds.filter(([at]) => at <= t - 20).at(-1)?.[1] ?? 0;
    return [t + 1, t > 1500 && t < 1600 ? 40 : 0, "follow", n, t > 1500 && t < 1540 ? 0 : 1] as [number, number, string, number, number];
  });
  const commits = ds.flatMap(([t]) => [t + 2, t + 5]);
  const r = { commits, deltas: ds, turnEnd: 1950, frames, samples };

  it("reports rates, frames and latency over the window", () => {
    const s = summarize(r, [[1200, 60], [5000, 80]]);
    expect(s.windowMs).toBe(950);
    expect(s.deltas).toBe(10);
    expect(s.commits).toBe(20);
    expect(s.commitsPerDelta).toBe(2);
    expect(s.frameP50).toBe(16);
    expect(s.longtasks).toBe(1);
    // Rendered at the first frame at least 20 ms after it: 21–36 ms.
    expect(s.onScreenP50).toBeGreaterThanOrEqual(21);
    expect(s.onScreenP95).toBeLessThanOrEqual(37);
    expect(s.offBottomPct).toBeGreaterThan(0);
    expect(s.offBottomMaxPx).toBe(40);
    expect(s.endHiddenPct).toBeGreaterThan(0);
    expect(s.leftFollow).toBe(0);
  });

  it("ends the window where asked (paced: the reveal's end) and measures from the proxy's send times", () => {
    const s = summarize(r, [], { to: 1500, sent: ds.map(([t]) => t - 5) });
    expect(s.windowMs).toBe(500);
    expect(s.wireOnScreenP50).toBe(s.onScreenP50 + 5);
    // The wrong number of send times: not reported.
    expect(Number.isNaN(summarize(r, [], { sent: [1, 2] }).wireOnScreenP50)).toBe(true);
  });
});

describe("proxyDeltaTimes", () => {
  it("takes the window's text_delta stamps from /output, on the page's clock, only when they all match", () => {
    const rec = { deltas: [[1000, 1], [1100, 2]] as [number, number][], turnEnd: 1150, clockOffset: 7 };
    const snap = {
      requests: [
        { sse: true, path: "/sessions/ses_1/output", deltaAt: [500, 995, 1103] },
        { sse: true, path: "/events", deltaAt: [1000] },
      ],
    } as unknown as ProxySnapshot;
    expect(proxyDeltaTimes(snap, rec)).toEqual([988, 1096]);
    expect(proxyDeltaTimes({ requests: [] } as unknown as ProxySnapshot, rec)).toBeUndefined();
  });
});

describe("traceBucket", () => {
  it("sorts the trace's event names into the spike's buckets", () => {
    expect(traceBucket("MinorGC")).toBe("gc");
    expect(traceBucket("V8.GC_SCAVENGER")).toBe("gc");
    expect(traceBucket("UpdateLayoutTree")).toBe("style");
    expect(traceBucket("Layout")).toBe("layout");
    expect(traceBucket("Paint")).toBe("paint");
    expect(traceBucket("CompositeLayers")).toBe("paint");
    expect(traceBucket("FunctionCall")).toBe("js");
    expect(traceBucket("v8.callFunction")).toBe("js");
    expect(traceBucket("RunTask")).toBe("other");
  });
});

describe("analyzeTrace", () => {
  const ev = (name: string, ts: number, dur?: number, ph = "X", tid = 1): TraceEvent => ({ name, ph, ts, dur, pid: 1, tid });
  // Window 1000–11000 µs. A task 2000–6000 with a function 2000–5000 holding a layout 4000–5000; a GC on another thread.
  const events = [
    ev("start", 1000, undefined, "R"),
    ev("end", 11000, undefined, "R"),
    ev("RunTask", 2000, 4000),
    ev("FunctionCall", 2000, 3000),
    ev("Layout", 4000, 1000),
    ev("RunTask", 10000, 3000),
    ev("MinorGC", 3000, 1000, "X", 2),
  ];

  it("counts busy time clipped to the window and self time by bucket, on the marks' thread only", () => {
    const t = analyzeTrace(events, "start", "end")!;
    expect(t.windowMs).toBe(10);
    // 4 ms + 1 ms (the second task clipped at 11000).
    expect(t.busyMs).toBe(5);
    expect(t.byBucket.js).toBe(2);
    expect(t.byBucket.layout).toBe(1);
    expect(t.byBucket.other).toBe(2);
    expect(t.byBucket.gc).toBe(0);
  });

  it("attributes the probe's own share by its sample() intervals", () => {
    // The probe's sample ran 4000–5000 µs: page ms 103–104 with the start mark at page 100.
    const t = analyzeTrace(events, "start", "end", { intervals: [[103, 104]], startPage: 100 })!;
    expect(t.probeMs!.layout).toBe(1);
    expect(t.probeMs!.js).toBe(0);
  });

  it("is undefined without both marks", () => {
    expect(analyzeTrace(events, "start", "nope")).toBeUndefined();
  });
});
