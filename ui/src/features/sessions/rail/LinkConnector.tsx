import styles from "./SessionRail.module.css";

/**
 * The line from a holder down into one of its linked sessions.
 *
 * Drawn as SVG rather than with borders on a pseudo-element, which is what
 * this was first: a border elbow can only turn through a border-radius, so
 * the curve is as tight as the radius and the head has to be a CSS triangle —
 * a 7px wedge that disappears at 1x even though it looks deliberate zoomed in.
 * A path can sweep properly, take a round cap, and end in a head that is
 * filled or stroked, which is what carries the two roles.
 *
 * Every coordinate is relative to the subject card's padding box, which is
 * where an absolutely-positioned child is placed from — so it already
 * excludes the card's 1px border, and each number below is one less than the
 * figure it aims at on screen:
 *
 *   x 21  the card's left border (so the head's tip lands on it)
 *   x 10  the trunk, 11px clear of the card
 *   y 23.5 the vertical centre of the name's first line (1px border + 8px
 *         padding + half the name's 16.875px line box — 12.5px at 1.35 — is
 *         17.44 from the card's top; less the 1px border, plus the 7px this
 *         box starts above the card, is 23.44, rounded to the half pixel).
 *         It used to aim at the status dot, which is gone (shell-dock D9).
 *         The first line, not the card's middle: the name clamps to two lines
 *         and a card's height varies, so only the top is a stable target.
 *
 * The continuation below a subject that has another after it is still a
 * border (.subjects .row:not(:last-child)::after) — it is a straight line
 * with nothing to curve, and one element beats one per row.
 */
export const LINK_ANCHOR_Y = 23.5;

export function LinkConnector({ watched }: { watched: boolean }) {
  return (
    <svg
      className={styles.linkConnector}
      viewBox="0 0 32 34"
      width="32"
      height="34"
      fill="none"
      aria-hidden="true"
    >
      {/* Quadratic with its control on the corner, so the tangent is vertical
          where it leaves the holder and horizontal where it meets the card: a
          quarter circle of radius 14, where a border-radius elbow managed 7. */}
      <path
        d={`M6 0 L6 10 Q6 ${LINK_ANCHOR_Y} 21 ${LINK_ANCHOR_Y}`}
        stroke="currentColor"
        strokeWidth="2.6"
        strokeLinecap="round"
      />
      {watched ? (
        // Open, and heavier than the line it ends. A monitor reads its
        // subject; the solid head is reserved for the one that drives it.
        <path
          d={`M22 ${LINK_ANCHOR_Y - 5.2} L28 ${LINK_ANCHOR_Y} L22 ${LINK_ANCHOR_Y + 5.2}`}
          stroke="currentColor"
          strokeWidth="3"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      ) : (
        <path d={`M29.5 ${LINK_ANCHOR_Y} L20.5 ${LINK_ANCHOR_Y - 5} L20.5 ${LINK_ANCHOR_Y + 5} Z`} fill="currentColor" />
      )}
    </svg>
  );
}
