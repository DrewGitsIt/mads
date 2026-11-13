import { describe, expect, it } from "vitest";
import { FakeSessionEventSource } from "../../router/fixtures";
import { BackgroundOutputBridge, BackgroundOutputSource, formatBytes } from "./backgroundOutput";

const b64 = (bytes: number[] | string) =>
  btoa(String.fromCharCode(...(typeof bytes === "string" ? new TextEncoder().encode(bytes) : bytes)));

describe("BackgroundOutputSource", () => {
  it("keeps output until a reader attaches, then feeds it live", () => {
    const es = new FakeSessionEventSource();
    const source = new BackgroundOutputSource(es);
    es.emitRaw("message", b64("one\n"), "4");
    const got: string[] = [];
    source.onData((d) => got.push(d));
    es.emitRaw("message", b64("two\n"), "8");
    expect(got).toEqual(["one\n", "two\n"]);
    expect(source.received).toBe(8);
  });

  it("decodes a UTF-8 character split across chunks whole", () => {
    const es = new FakeSessionEventSource();
    const source = new BackgroundOutputSource(es);
    let text = "";
    source.onData((d) => (text += d));
    const e = [...new TextEncoder().encode("é")]; // two bytes
    es.emitRaw("message", b64([0x61, e[0]]));
    es.emitRaw("message", b64([e[1], 0x62]));
    expect(text).toBe("aéb");
  });

  it("records how much was trimmed, and a reset clears the view", () => {
    const es = new FakeSessionEventSource();
    const source = new BackgroundOutputSource(es);
    let text = "";
    source.onData((d) => (text += d));
    es.emitRaw("trimmed", "123456");
    es.emitRaw("message", b64("tail\n"));
    expect(source.trimmed).toBe(123456);
    es.emitRaw("reset", "");
    expect(text.endsWith("\x1bc")).toBe(true);
    expect(source.trimmed).toBe(0);
    expect(source.received).toBe(0);
  });

  it.each([
    ["ended", "ended"],
    ["gone", "gone"],
    ["none", "none"],
    ["something new", "none"],
  ])("end %j reads as %s and closes the stream", (data, want) => {
    const es = new FakeSessionEventSource();
    const source = new BackgroundOutputSource(es);
    let changes = 0;
    source.onState(() => changes++);
    es.emitRaw("end", data);
    expect(source.endReason).toBe(want);
    expect(es.closed).toBe(true);
    expect(changes).toBe(1);
  });

  it("is not closed by the terminal killing its bridge (StrictMode's rehearsal unmount)", () => {
    const es = new FakeSessionEventSource();
    const bridge = new BackgroundOutputBridge(new BackgroundOutputSource(es));
    bridge.kill();
    expect(es.closed).toBe(false);
  });
});

describe("formatBytes", () => {
  it.each([
    [312, "312 bytes"],
    [64 * 1024, "64 KB"],
    [1.25 * 1024 * 1024, "1.3 MB"],
  ])("%d → %s", (n, want) => expect(formatBytes(n)).toBe(want));
});
