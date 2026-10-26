import { describe, expect, it } from "vitest";
import { ANCHOR_GAP_PX, pageSize, runwayFor, trimmedCap } from "./runway";

// A 500px usable view with the anchor at 1012 (so its gap puts the top at 1000).
const base = { anchorTop: 1000 + ANCHOR_GAP_PX, usable: 500, cap: null };
const page = pageSize(500); // 436
// scrollTop when pinned at the bottom.
const pinnedTop = (r: { floor: number }, usable = 500) => r.floor - usable;

describe("runwayFor", () => {
  it("reserves the rest of the screen under the anchor", () => {
    const r = runwayFor({ ...base, tail: 1100 });
    expect(r).toEqual({ pages: 0, floor: 1500, spacer: 400 });
    expect(pinnedTop(r)).toBe(1000);
  });

  it("holds still while the reply fills the first screen", () => {
    const floors = [1100, 1300, 1480].map((tail) => runwayFor({ ...base, tail }).floor);
    expect(new Set(floors).size).toBe(1);
  });

  it("turns one page once the reply reaches the bottom edge, keeping ~3 lines", () => {
    const r = runwayFor({ ...base, tail: 1495 });
    expect(r.pages).toBe(1);
    expect(pinnedTop(r)).toBe(1000 + page);
    // The edge the reply crossed is now near the top: about three lines in.
    expect(1492 - pinnedTop(r)).toBe(500 - page - 8);
  });

  it("is a function of position: a burst of two pages lands where paging twice would", () => {
    const stepped = runwayFor({ ...base, tail: 1000 + 500 + page + 20 });
    expect(stepped.pages).toBe(2);
    expect(pinnedTop(stepped)).toBe(1000 + 2 * page);
  });

  it("keeps the pinned position when the usable height changes (a composer grows)", () => {
    const a = runwayFor({ ...base, tail: 1100 });
    const b = runwayFor({ ...base, tail: 1100, usable: 480 });
    expect(pinnedTop(a)).toBe(pinnedTop(b, 480));
  });

  it("never reserves less than nothing when the reply runs past the floor", () => {
    expect(runwayFor({ ...base, tail: 1100, cap: 1050 }).spacer).toBe(0);
  });

  it("lowers the floor to a cap", () => {
    expect(runwayFor({ ...base, tail: 1100, cap: 1200 })).toMatchObject({ floor: 1200, spacer: 100 });
  });

  it("reserves nothing before the stream has a size", () => {
    expect(runwayFor({ ...base, tail: 1100, usable: 0 }).spacer).toBe(0);
  });
});

describe("trimmedCap", () => {
  it("drops space below the viewport, keeps space in it", () => {
    expect(trimmedCap(1500, 1100, 1300)).toBe(1300);
  });
  it("never cuts into the reply", () => {
    expect(trimmedCap(1500, 1100, 900)).toBe(1100);
  });
  it("never raises the floor", () => {
    expect(trimmedCap(1500, 1100, 1700)).toBe(1500);
  });
});
