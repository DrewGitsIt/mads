/**
 * The reading runway's geometry (docs/CHAT-RUNWAY-DESIGN.md). Pure: every
 * number is in the stream's content coordinates, measured by useRunway.
 *
 * The runway is a spacer at the end of the stream, sized so the scroller's
 * bottom *is* where the reader should be: the latest user-role message at
 * the top, the reply filling the space under it, a page turn each time the
 * reply reaches the bottom edge. useStickToBottom keeps pinning to the
 * bottom as it always has; this only decides how far away the bottom is.
 */

/**
 * What ChatRenderer stamps on a runway anchor: a person's message (the
 * user's, or one from a linked session). `live`: it arrived while the stream
 * was open, so its runway glides in; otherwise it was history.
 */
export type RunwayAnchorAttrs = { "data-runway-anchor"?: string; "data-runway-live"?: string };

/** Space kept above the anchor message when it sits at the top. */
export const ANCHOR_GAP_PX = 12;
/** What a page turn keeps in view from the page before: about three lines. */
export const CONTEXT_PX = 64;
/** The reply turns the page once it's this close to the bottom edge. */
const EDGE_PX = 8;

export interface RunwayInput {
  /** Top of the anchor (the latest user-role message). */
  anchorTop: number;
  /** End of the reply: where the spacer starts. */
  tail: number;
  /** The height text can be read in: the viewport above the pinned cluster. */
  usable: number;
  /** Trim and settle lower the floor to this; null = not lowered. */
  cap: number | null;
}

export interface Runway {
  /** Pages turned since the anchor (0 = the first screen). */
  pages: number;
  /** Where the spacer ends: the bottom the lock pins to. */
  floor: number;
  spacer: number;
}

/** One page turn's distance: a screen less the lines kept for context. */
export function pageSize(usable: number): number {
  return Math.max(usable - CONTEXT_PX, usable / 2);
}

/**
 * Depends only on where things are, not on what happened: a reader coming
 * back to the bottom after scrolling away lands on the page they'd be on had
 * they never left. At the bottom scrollTop = anchor − gap + pages × page,
 * which doesn't depend on `usable`, so a growing composer moves nothing.
 */
export function runwayFor({ anchorTop, tail, usable, cap }: RunwayInput): Runway {
  if (!(usable > 0)) return { pages: 0, floor: tail, spacer: 0 };
  const top = anchorTop - ANCHOR_GAP_PX;
  const over = tail - (top + usable - EDGE_PX);
  const pages = over > 0 ? Math.ceil(over / pageSize(usable)) : 0;
  const floor = Math.min(top + pages * pageSize(usable) + usable, cap ?? Infinity);
  return { pages, floor, spacer: Math.max(0, Math.round(floor - tail)) };
}

/**
 * Trim: the floor after the reader's viewport bottom moved to `viewBottom`.
 * Space below the viewport goes; space in it stays, so nothing on screen
 * moves. Only ever lowers.
 */
export function trimmedCap(floor: number, tail: number, viewBottom: number): number {
  return Math.min(floor, Math.max(tail, viewBottom));
}
