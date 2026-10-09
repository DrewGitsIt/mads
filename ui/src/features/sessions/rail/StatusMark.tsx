import styles from "./SessionRail.module.css";
import type { RailStatusKind } from "./railGroups";

/**
 * The status line's leading mark (docs/mockups/rail-status-dimensions.html).
 *
 * One drawing system, not six: every mark is a path in the same 10px box at
 * the same 1.6px stroke. The marks this replaced were borrowed from the UI
 * font — ◦ ✓ ✕ ▲ plus ◌ at a patched-up 11px — and a typeface decides what
 * those weigh. For the two calm states it decided "almost nothing", which is
 * why the rail's left edge had two bright marks, two faint ones and two gaps
 * instead of a column. The family here is the BOX AND THE STROKE WEIGHT, not
 * the silhouette, which is what lets a triangle and a tick belong to it.
 *
 * Three independent channels:
 *
 *   shape    what the session is doing
 *   colour   whether it wants you — set by the .status-* tone on the parent,
 *            so every path paints with currentColor and none names a hue
 *   motion   whether it is moving. Exactly one state animates, and it is the
 *            one that is actually moving: `working`. `needs` deliberately
 *            does NOT breathe (the old .statusBell did) — a filled warn
 *            triangle is unmistakable standing still, and an exception here
 *            would cost the rule its whole meaning.
 */
export function StatusMark({ kind }: { kind: RailStatusKind }) {
  return (
    <svg
      className={[styles.statusMark, kind === "working" ? styles.statusMarkSpin : ""]
        .filter(Boolean)
        .join(" ")}
      viewBox="0 0 10 10"
      width="10"
      height="10"
      fill="none"
      aria-hidden="true"
    >
      {MARKS[kind]}
    </svg>
  );
}

/** r 3.6 at stroke 1.6 puts the outer edge at 4.4 — the box's full width, with the
    0.8 half-stroke on each side doing the optical work a 1px hairline could not. */
const RING_R = 3.6;
const STROKE = 1.6;

/* A 6-unit dash on a ~22.6-unit circumference is a ~95° arc: short enough to
   read as a sweep rather than as a ring with a nick in it, which matters now
   that `done` is a closed ring of the same radius. */
const ARC = { r: RING_R, strokeWidth: STROKE, strokeLinecap: "round", strokeDasharray: "6 17" } as const;

const MARKS: Record<RailStatusKind, React.JSX.Element> = {
  // A spinner, and the only thing on the rail that moves.
  working: (
    <>
      <circle className={styles.statusMarkTrack} cx="5" cy="5" r={RING_R} strokeWidth={STROKE} />
      <circle className={styles.statusMarkArc} cx="5" cy="5" stroke="currentColor" {...ARC} />
    </>
  ),
  // The same spinner, stopped. "A spinner that stopped" is exactly what the
  // old ◌ was reaching for and could only gesture at; drawing the working
  // geometry and withholding the animation says it outright — and lifts the
  // mark to the weight its bucket deserves, since filterBucketOf files a
  // stalled session under "Needs you" and ◌ was the faintest thing here.
  stalled: (
    <>
      <circle className={styles.statusMarkTrack} cx="5" cy="5" r={RING_R} strokeWidth={STROKE} />
      <circle cx="5" cy="5" stroke="currentColor" transform="rotate(-100 5 5)" {...ARC} />
    </>
  ),
  // Filled, with a 1.3px round join: at 10px a bare miter goes needle-sharp
  // at the apex and reads lighter than the rings beside it.
  needs: (
    <path
      d="M5 1.1 L9.3 8.7 L0.7 8.7 Z"
      fill="currentColor"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinejoin="round"
    />
  ),
  crashed: (
    <path
      d="M2.1 2.1 L7.9 7.9 M7.9 2.1 L2.1 7.9"
      stroke="currentColor"
      strokeWidth={STROKE}
      strokeLinecap="round"
    />
  ),
  // The working arc, gone all the way round and closed. Not a tick: ✓ now
  // belongs to the Set-aside control, where it means a judgement the user
  // made rather than a state the session reports. This only reads because
  // `idle` left the ring — against a ring-shaped idle the pair was separated
  // by hue alone, which is far too much weight on colour for the one resting
  // signal that means something is waiting for you.
  done: <circle cx="5" cy="5" r={RING_R} stroke="currentColor" strokeWidth={STROKE} />,
  // A dial at 4 o'clock: at rest, and some time has passed.
  //
  // The rim is 1.3px, not the family's 1.6 — a deliberate exception, and the
  // reason is the rule's own: marks should carry equal OPTICAL weight, and a
  // circle with a stroke inside it is already heavier than a bare ring at the
  // same gauge. At 1.6 the interior is ~5.0px across and the hand's round cap
  // lands within a fraction of the rim, so the two merge into a blob.
  //
  // One hand, not two. A clock face is the most repeated mark on the rail —
  // idle is most of twenty sessions most of the time — and two strokes inside
  // a rim, fifteen times down a column, stops receding and starts competing
  // with the four marks that matter. One hand at an angle in a circle is
  // enough to say clock.
  idle: (
    <>
      <circle cx="5" cy="5" r="3.5" stroke="currentColor" strokeWidth="1.3" />
      <path d="M5 5 L6.3 5.75" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </>
  ),
};
