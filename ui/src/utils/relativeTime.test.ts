import { afterEach, describe, expect, it, vi } from "vitest";
import { formatAbsoluteStamp, formatCalendarStamp, formatRelativeStamp } from "./relativeTime";

/** Fixed local reference point: Monday 28 Sep 2026, 13:40:00. */
const NOW = new Date(2026, 8, 28, 13, 40, 0).getTime();

/** Locale-agnostic expectations for the two named-day buckets. */
const weekday = (d: Date) => d.toLocaleDateString(undefined, { weekday: "short" });
const month = (d: Date) => d.toLocaleDateString(undefined, { month: "short" });

const at = (y: number, m: number, d: number, h = 0, min = 0, s = 0) =>
  new Date(y, m, d, h, min, s).toISOString();

const ago = (ms: number) => new Date(NOW - ms).toISOString();

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

describe("formatRelativeStamp", () => {
  afterEach(() => vi.useRealTimers());

  it("reads 'now' below a minute, on both sides of the boundary", () => {
    expect(formatRelativeStamp(ago(0), NOW)).toBe("now");
    expect(formatRelativeStamp(ago(59 * SECOND + 999), NOW)).toBe("now");
    expect(formatRelativeStamp(ago(MINUTE), NOW)).toBe("1m");
  });

  it("counts whole minutes up to the hour boundary", () => {
    expect(formatRelativeStamp(ago(14 * MINUTE), NOW)).toBe("14m");
    expect(formatRelativeStamp(ago(59 * MINUTE + 59 * SECOND), NOW)).toBe("59m");
  });

  it("switches to a clock time at an hour, for anything still today", () => {
    expect(formatRelativeStamp(ago(HOUR), NOW)).toBe("12:40");
    expect(formatRelativeStamp(at(2026, 8, 28, 9, 2), NOW)).toBe("09:02");
    expect(formatRelativeStamp(at(2026, 8, 28, 0, 0), NOW)).toBe("00:00");
  });

  it("names the day for anything earlier this week", () => {
    const yesterday = new Date(2026, 8, 27, 9, 14);
    const sixDays = new Date(2026, 8, 22, 9, 14);
    expect(formatRelativeStamp(yesterday.toISOString(), NOW)).toBe(`${weekday(yesterday)} 09:14`);
    expect(formatRelativeStamp(sixDays.toISOString(), NOW)).toBe(`${weekday(sixDays)} 09:14`);
  });

  it("falls back to day-and-month once a week has passed", () => {
    const justOverAWeek = new Date(NOW - 7 * DAY - MINUTE);
    expect(formatRelativeStamp(justOverAWeek.toISOString(), NOW)).toBe(
      `${justOverAWeek.getDate()} ${month(justOverAWeek)}`,
    );
    const older = new Date(2026, 8, 4, 8, 0);
    expect(formatRelativeStamp(older.toISOString(), NOW)).toBe(`4 ${month(older)}`);
  });

  it("reads 'now' for a future stamp rather than a negative count", () => {
    expect(formatRelativeStamp(new Date(NOW + 5 * MINUTE).toISOString(), NOW)).toBe("now");
  });

  it("returns an empty string for a missing or unparseable stamp", () => {
    expect(formatRelativeStamp(undefined, NOW)).toBe("");
    expect(formatRelativeStamp("", NOW)).toBe("");
    expect(formatRelativeStamp("not a date", NOW)).toBe("");
  });

  it("defaults to the current time when no reference is passed", () => {
    vi.useFakeTimers({ now: NOW });
    expect(formatRelativeStamp(ago(14 * MINUTE))).toBe("14m");
  });
});

describe("formatCalendarStamp", () => {
  it("spells out today, this week and older the way the card footer reads", () => {
    expect(formatCalendarStamp(at(2026, 8, 28, 13, 2), NOW)).toBe("today 13:02");
    const thisWeek = new Date(2026, 8, 25, 9, 14);
    expect(formatCalendarStamp(thisWeek.toISOString(), NOW)).toBe(`${weekday(thisWeek)} 09:14`);
    const older = new Date(2026, 8, 4, 8, 0);
    expect(formatCalendarStamp(older.toISOString(), NOW)).toBe(`4 ${month(older)}`);
  });

  it("returns an empty string for an unparseable stamp", () => {
    expect(formatCalendarStamp("not a date", NOW)).toBe("");
  });
});

describe("formatAbsoluteStamp", () => {
  it("renders the whole local instant, zero-padded", () => {
    expect(formatAbsoluteStamp(at(2026, 8, 4, 9, 2, 7))).toBe("2026-09-04 09:02:07");
  });

  it("returns an empty string for an unparseable stamp", () => {
    expect(formatAbsoluteStamp(undefined)).toBe("");
  });
});
