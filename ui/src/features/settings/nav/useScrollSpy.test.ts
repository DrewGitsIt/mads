import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useScrollSpy, type SectionRefs } from "./useScrollSpy";
import { SECTION_IDS } from "../sections";

function stubGeometry(el: HTMLElement, top: number) {
  Object.defineProperty(el, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ top, left: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0, toJSON() {} }),
  });
}

describe("useScrollSpy", () => {
  it("attaches its listener once the container mounts on a later render, not just the first", () => {
    // Regression for a mount-timing bug: SettingsSurface renders a loading
    // placeholder (containerRef.current === null) before the real page, and
    // containerRef/sectionRefs are stable ref objects whose .current mutating
    // does not itself trigger a rerender — only a real state flip (`ready`)
    // does. If the effect's dependency array omits `ready`, it runs once
    // while the container is still unmounted, bails out, and never
    // re-attaches — scroll-spy then stays frozen on SECTION_IDS[0] forever.
    type MutableSectionRefs = Record<(typeof SECTION_IDS)[number], { current: HTMLElement | null }>;
    const containerRef = { current: null as HTMLElement | null };
    const mutableSectionRefs = Object.fromEntries(
      SECTION_IDS.map((id) => [id, { current: null as HTMLElement | null }]),
    ) as MutableSectionRefs;
    const sectionRefs = mutableSectionRefs as SectionRefs;

    const { result, rerender } = renderHook(
      ({ ready }: { ready: boolean }) => useScrollSpy(containerRef, sectionRefs, ready),
      { initialProps: { ready: false } },
    );

    expect(result.current).toBe(SECTION_IDS[0]);

    const container = document.createElement("div");
    stubGeometry(container, 0);
    Object.defineProperty(container, "scrollTop", { configurable: true, value: 3000 });
    Object.defineProperty(container, "clientHeight", { configurable: true, value: 700 });
    Object.defineProperty(container, "scrollHeight", { configurable: true, value: 3300 });
    containerRef.current = container;
    SECTION_IDS.forEach((id, i) => {
      const section = document.createElement("div");
      stubGeometry(section, i * 400);
      mutableSectionRefs[id].current = section;
    });

    rerender({ ready: true });

    // scrollTop + clientHeight (3700) >= scrollHeight - epsilon (3299): at bottom,
    // so the last section must be active — the exact "bottom of page" case.
    expect(result.current).toBe(SECTION_IDS[SECTION_IDS.length - 1]);
  });
});

describe("useScrollSpy, layout changes", () => {
  it("follows sections that move without a scroll (content loading above or in them)", async () => {
    const observers: (() => void)[] = [];
    class FakeResizeObserver {
      constructor(cb: () => void) {
        observers.push(cb);
      }
      observe() {}
      disconnect() {}
    }
    const prev = globalThis.ResizeObserver;
    globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver;
    try {
      const container = document.createElement("div");
      stubGeometry(container, 0);
      Object.defineProperty(container, "scrollTop", { configurable: true, value: 1000 });
      Object.defineProperty(container, "clientHeight", { configurable: true, value: 1000 });
      Object.defineProperty(container, "scrollHeight", { configurable: true, value: 10_000 });
      const tops = new Map<string, number>(SECTION_IDS.map((id, i) => [id, i * 400 - 1000]));
      const refs = Object.fromEntries(
        SECTION_IDS.map((id) => {
          const el = document.createElement("div");
          Object.defineProperty(el, "getBoundingClientRect", {
            configurable: true,
            value: () => ({ top: tops.get(id)!, left: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0, toJSON() {} }),
          });
          return [id, { current: el }];
        }),
      ) as unknown as SectionRefs;
      const { result } = renderHook(() => useScrollSpy({ current: container }, refs, true));
      // Reading line at 1000 + 300: the section whose top is 1200 (index 3).
      expect(result.current).toBe(SECTION_IDS[3]);
      // Paired devices' content lands: everything from it down moves up
      // into the reading line, with no scroll event.
      const devices = SECTION_IDS.indexOf("devices");
      for (const [i, id] of SECTION_IDS.entries()) if (i > devices) tops.set(id, tops.get(id)! + 600);
      tops.set("devices", 1250 - 1000);
      const { act } = await import("@testing-library/react");
      act(() => observers.forEach((cb) => cb()));
      expect(result.current).toBe("devices");
    } finally {
      globalThis.ResizeObserver = prev;
    }
  });
});
