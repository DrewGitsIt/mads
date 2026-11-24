import { afterEach, describe, expect, it, vi } from "vitest";
import { formatMessageTime, messageStamp, parseMessageTime } from "./messageTime";

describe("messageStamp", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("gives what a <time> showed before: dateTime, the local title, the short label", () => {
    const ts = "2026-09-29T21:52:23Z";
    const at = parseMessageTime(ts)!;
    const stamp = messageStamp(ts)!;
    expect(stamp.iso).toBe(at.toISOString());
    expect(stamp.title).toBe(at.toLocaleString());
    expect(stamp.label).toBe(formatMessageTime(at));
  });

  it("formats a timestamp once", () => {
    expect(messageStamp("2026-09-28T10:00:00Z")).toBe(messageStamp("2026-09-28T10:00:00Z"));
  });

  it("has no stamp for a missing or zero time", () => {
    expect(messageStamp(undefined)).toBeUndefined();
    expect(messageStamp("0001-01-01T00:00:00Z")).toBeUndefined();
    expect(messageStamp("not a time")).toBeUndefined();
  });

  it("drops the day once the stamp is today, and adds it back past midnight", () => {
    vi.useFakeTimers();
    const at = new Date(2026, 8, 29, 22, 59);
    vi.setSystemTime(new Date(2026, 8, 28, 12, 0));
    const ts = at.toISOString();
    expect(messageStamp(ts)!.label).toMatch(/Sep 29/);
    vi.setSystemTime(new Date(2026, 8, 29, 23, 30));
    expect(messageStamp(ts)!.label).not.toMatch(/Sep/);
    expect(formatMessageTime(at)).toBe(messageStamp(ts)!.label);
    vi.setSystemTime(new Date(2026, 8, 30, 0, 1));
    expect(messageStamp(ts)!.label).toMatch(/Sep 29/);
    expect(formatMessageTime(at)).toBe(messageStamp(ts)!.label);
  });
});
