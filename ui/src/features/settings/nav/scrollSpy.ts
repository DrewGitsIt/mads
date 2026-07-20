import type { SectionId } from "../sections";

export interface ScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

export interface SectionTop {
  id: SectionId;
  top: number;
}

/** Fraction of the viewport, from the top, the reading line sits at. */
export const READING_LINE_FRACTION = 0.3;

/** True once the container is scrolled to (or past) its bottom edge. */
export function isAtBottom(metrics: ScrollMetrics, epsilon = 1): boolean {
  return metrics.scrollTop + metrics.clientHeight >= metrics.scrollHeight - epsilon;
}

/**
 * Active section = the last section (in page order) whose top has scrolled
 * past the reading line. At the bottom of the page the last section is
 * always active, even if its top never reaches the reading line — short
 * trailing sections would otherwise be unreachable in the nav rail.
 * `sections` must be sorted in page (top-ascending) order.
 */
export function computeActiveSection(
  metrics: ScrollMetrics,
  sections: SectionTop[],
): SectionId | null {
  if (sections.length === 0) return null;
  if (isAtBottom(metrics)) return sections[sections.length - 1].id;

  const readingLine = metrics.scrollTop + metrics.clientHeight * READING_LINE_FRACTION;
  let active = sections[0].id;
  for (const section of sections) {
    if (section.top <= readingLine) {
      active = section.id;
    } else {
      break;
    }
  }
  return active;
}
