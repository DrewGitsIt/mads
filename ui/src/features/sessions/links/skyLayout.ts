/**
 * The link modal's sky geometry (docs/mockups/link-modal-constellation.html,
 * Many nodes → "Wrap: rows of 4"). Pure, so it can be tested without a DOM.
 *
 * The centred session sits in the middle; its holders sit above and its
 * subjects below, each side chunked into rows of at most PER, in order. Row 0
 * is nearest the centre and further rows stack outward, so the sky grows
 * taller rather than wider. A line to a row past the first leaves the centre,
 * curves into the nearest gutter between two row-0 nodes, runs straight
 * through it, and curves into its node, so it never crosses a nearer node.
 */

/** Sky width; the modal is sized to fit it. */
export const SKY_W = 640;
/** Nodes per row before wrapping. */
export const PER = 4;
/** Horizontal spacing between node centres in a row. */
export const SP = 152;
/** A node's width; SP - NODE_W is the gutter between neighbours. */
export const NODE_W = 132;
/** Distance from the centred session to row 0 on either side. */
export const GAP_F = 130;
/** Distance between rows on one side: room for a node, the next row's badge, and air. */
export const ROWG = 84;
/** Space above the outermost holder row and below the outermost subject row. */
export const MARGIN = 70;
/** Where lines meet the centred session and an outer node, from their centres. */
export const FOCUS_HALF = 28;
export const NODE_HALF = 22;
/** A badge sits this far from its outer node's centre, toward the centre. */
export const BADGE_OFF = NODE_HALF + 16;
/** Lines sharing a gutter are offset by this much each, alternating sides. */
export const GUTTER_STEP = 5;

export interface Point {
  x: number;
  y: number;
}

export type Side = "up" | "down";

export interface Placed<T> {
  item: T;
  side: Side;
  /** 0 is nearest the centred session. */
  row: number;
  /** Node centre. */
  x: number;
  y: number;
  /** The line's points, holder first, so dash animations keep their direction. */
  wire: Point[];
  badge: Point;
}

export interface SkyLayout<T> {
  width: number;
  height: number;
  focus: Point;
  /** Where an empty side's ghost node sits. */
  ghostUp: Point;
  ghostDown: Point;
  placed: Placed<T>[];
}

export function chunk<T>(items: readonly T[], n: number): T[][] {
  const rows: T[][] = [];
  for (let i = 0; i < items.length; i += n) rows.push(items.slice(i, i + n));
  return rows;
}

/** Centres of a row of k nodes, centred on the sky. */
export function rowXs(k: number, width: number = SKY_W): number[] {
  return Array.from({ length: k }, (_, i) => width / 2 + (i - (k - 1) / 2) * SP);
}

/** An SVG path through points, vertical at every bend, so lines leave and enter nodes vertically. */
export function curve(pts: readonly Point[]): string {
  return pts.reduce((d, p, i) => {
    if (i === 0) return `M ${p.x} ${p.y}`;
    const q = pts[i - 1];
    const ym = (q.y + p.y) / 2;
    return q.x === p.x ? `${d} L ${p.x} ${p.y}` : `${d} C ${q.x} ${ym}, ${p.x} ${ym}, ${p.x} ${p.y}`;
  }, "");
}

export function layoutSky<T>(ups: readonly T[], downs: readonly T[]): SkyLayout<T> {
  const width = SKY_W;
  const upRows = chunk(ups, PER);
  const downRows = chunk(downs, PER);
  const U = Math.max(1, upRows.length);
  const D = Math.max(1, downRows.length);
  const fy = MARGIN + (U - 1) * ROWG + GAP_F;
  const fx = width / 2;
  const height = fy + GAP_F + (D - 1) * ROWG + MARGIN;
  const placed: Placed<T>[] = [];

  const lay = (rows: T[][], side: Side) => {
    const dir = side === "down" ? 1 : -1;
    const xs0 = rowXs(rows[0]?.length ?? 0, width);
    const gutters = xs0.slice(1).map((x, i) => (x + xs0[i]) / 2);
    const start = { x: fx, y: fy + dir * FOCUS_HALF };
    // First pass: each far node takes its nearest gutter; a node midway
    // between two takes the less used, so lines spread out.
    const load = gutters.map(() => 0);
    const nodes = rows.flatMap((row, r) => {
      const xs = rowXs(row.length, width);
      const y = fy + dir * (GAP_F + r * ROWG);
      return row.map((item, i) => {
        const x = xs[i];
        let g = -1;
        if (r > 0) {
          // Rows past the first are only reached when row 0 is full, so it has gutters.
          const d = gutters.map((gx) => Math.abs(gx - x));
          const best = Math.min(...d);
          for (let j = 0; j < gutters.length; j++) {
            if (d[j] - best < 0.5 && (g < 0 || load[j] < load[g])) g = j;
          }
          load[g]++;
        }
        return { item, r, x, y, g };
      });
    });
    // Second pass: lines sharing a gutter alternate either side of its centre,
    // GUTTER_STEP apart, closer when a crowded gutter would push one onto a node.
    const room = (SP - NODE_W) / 2 - 3;
    const used = gutters.map(() => 0);
    for (const { item, r, x, y, g } of nodes) {
      const end = { x, y: y - dir * NODE_HALF };
      let pts: Point[] = [start, end];
      if (g >= 0) {
        const k = used[g]++;
        const step = Math.min(GUTTER_STEP, room / Math.max(1, Math.ceil((load[g] - 1) / 2)));
        const gx = gutters[g] + (k % 2 ? 1 : -1) * Math.ceil(k / 2) * step;
        const y0 = fy + dir * (GAP_F - NODE_HALF - 14);
        const y1 = y - dir * BADGE_OFF;
        pts = [start, { x: gx, y: y0 }, { x: gx, y: y1 }, end];
      }
      placed.push({
        item,
        side,
        row: r,
        x,
        y,
        wire: dir < 0 ? pts.reverse() : pts,
        badge: { x, y: y - dir * BADGE_OFF },
      });
    }
  };
  lay(upRows, "up");
  lay(downRows, "down");

  return {
    width,
    height,
    focus: { x: fx, y: fy },
    ghostUp: { x: fx, y: fy - GAP_F },
    ghostDown: { x: fx, y: fy + GAP_F },
    placed,
  };
}
