import { useEffect, useState } from "react";
import type { RefObject } from "react";
import { SECTION_IDS, type SectionId } from "../sections";
import { computeActiveSection, type SectionTop } from "./scrollSpy";

export type SectionRefs = Record<SectionId, RefObject<HTMLElement | null>>;

/**
 * Scroll-position-aware active section, computed against `containerRef`'s
 * own scroll (Settings scrolls an internal column, not the window — see
 * SettingsSurface.module.css .body). Wires the pure computeActiveSection
 * (nav/scrollSpy.ts) to real DOM measurements on scroll/resize.
 *
 * `ready` must flip false→true once containerRef/sectionRefs are actually
 * attached (e.g. once a loading gate has rendered the real content) —
 * containerRef/sectionRefs are stable ref objects, so mutating `.current`
 * doesn't by itself cause this effect to rerun. Without `ready` in the
 * dependency array, an effect that first runs while the container is still
 * unmounted (`.current === null`) would bail out via the guard below and
 * then never re-attach the scroll listener once the real DOM landed.
 */
export function useScrollSpy(
  containerRef: RefObject<HTMLElement | null>,
  sectionRefs: SectionRefs,
  ready: boolean,
): SectionId {
  const [active, setActive] = useState<SectionId>(SECTION_IDS[0]);

  useEffect(() => {
    const container = containerRef.current;
    if (!ready || !container) return;

    const update = () => {
      const containerTop = container.getBoundingClientRect().top;
      const sections: SectionTop[] = [];
      for (const id of SECTION_IDS) {
        const el = sectionRefs[id].current;
        if (!el) continue;
        sections.push({
          id,
          top: el.getBoundingClientRect().top - containerTop + container.scrollTop,
        });
      }
      const next = computeActiveSection(
        {
          scrollTop: container.scrollTop,
          clientHeight: container.clientHeight,
          scrollHeight: container.scrollHeight,
        },
        sections,
      );
      if (next) setActive(next);
    };

    update();
    container.addEventListener("scroll", update, { passive: true });
    window.addEventListener("resize", update);
    return () => {
      container.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
    };
  }, [containerRef, sectionRefs, ready]);

  return active;
}
