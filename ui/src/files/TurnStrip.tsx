import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import type { ChangeTurn } from "../router/types";
import { usePrefersReducedMotion } from "../tasks/GraphView/usePrefersReducedMotion";
import { clipPrompt, nextTurnSelection, stepTurn, stepTurnSelection, turnByN } from "./changesModel";
import styles from "./TurnStrip.module.css";

export interface TurnStripProps {
  turns: ChangeTurn[];
  /** "filter": clicks select turns that filter the list. "navigate" (Turn grouping): clicks scroll the list. */
  mode: "filter" | "navigate";
  /** Filter mode: the selected turns (a contiguous range), or null for all. */
  selected: number[] | null;
  onSelect: (turns: number[] | null) => void;
  /** Navigate mode: the turn in view (scroll-spy). */
  spy: number | null;
  onNavigate: (n: number) => void;
}

/** The columns out of view past each edge: more than half of a column past it counts. */
function outOfView(scroller: HTMLElement): { left: number; right: number } {
  const lo = scroller.scrollLeft;
  const hi = lo + scroller.clientWidth;
  let left = 0;
  let right = 0;
  for (const col of scroller.children as HTMLCollectionOf<HTMLElement>) {
    const mid = col.offsetLeft + col.offsetWidth / 2;
    if (mid < lo) left++;
    else if (mid > hi) right++;
  }
  return { left, right };
}

/** The first and last turn in view, or null when nothing is laid out (jsdom). */
function visibleEdges(scroller: HTMLElement): [number, number] | null {
  const lo = scroller.scrollLeft;
  const hi = lo + scroller.clientWidth;
  const shown = [...(scroller.children as HTMLCollectionOf<HTMLElement>)].filter((col) => {
    const mid = col.offsetLeft + col.offsetWidth / 2;
    return col.offsetWidth > 0 && mid >= lo && mid <= hi;
  });
  if (!shown.length) return null;
  return [Number(shown[0].dataset.turnCol), Number(shown[shown.length - 1].dataset.turnCol)];
}

/**
 * The turn strip (docs/mockups/changes-v2.html): one column per user turn,
 * bar height = files that turn touched, a flat stub for zero-file turns. The
 * whole column is the hit target. Hovering previews a turn's prompt in the
 * context block to the right of the bars; clicking pins it.
 *
 * Long sessions (docs/mockups/turn-strip-window.html): the columns get at
 * most half the strip and scroll; each edge with turns past it fades and
 * counts them under its arrow. The strip opens at turn 1 with nothing
 * selected, so the story reads from the start at the user's pace.
 */
export function TurnStrip({ turns, mode, selected, onSelect, spy, onNavigate }: TurnStripProps) {
  const [hover, setHover] = useState<number | null>(null);
  const scrollerRef = useRef<HTMLDivElement>(null);
  const [hidden, setHidden] = useState({ left: 0, right: 0 });
  const reduceMotion = usePrefersReducedMotion();
  const order = turns.map((t) => t.n);
  const max = Math.max(1, ...turns.map((t) => t.files));
  const filter = mode === "filter";
  const sel = filter ? selected : null;
  const current = filter ? sel : spy !== null ? [spy] : null;
  const lo = current?.[0] ?? null;
  const hi = current ? current[current.length - 1] : null;

  const measure = useCallback(() => {
    const scroller = scrollerRef.current;
    if (!scroller) return;
    const next = outOfView(scroller);
    setHidden((h) => (h.left === next.left && h.right === next.right ? h : next));
  }, []);

  // Keep the selected / in-view turn visible, moving the window only as far
  // as it takes: one column per arrow step. A neighbour stays in view past
  // it, so the edge fade lands on the neighbour rather than the selection.
  const focusTurn = filter ? (sel ? sel[sel.length - 1] : null) : spy;
  const reveal = useCallback(
    (smooth: boolean) => {
      const scroller = scrollerRef.current;
      if (!scroller || focusTurn === null) return;
      const col = scroller.querySelector<HTMLElement>(`[data-turn-col="${focusTurn}"]`);
      if (!col) return;
      const pitch = col.offsetWidth + 2; // .cols gap
      const left = col.offsetLeft - (col.previousElementSibling ? pitch : 0);
      const right = col.offsetLeft + col.offsetWidth + (col.nextElementSibling ? pitch : 0);
      let to: number | null = null;
      if (left < scroller.scrollLeft) to = left;
      else if (right > scroller.scrollLeft + scroller.clientWidth) to = right - scroller.clientWidth;
      if (to !== null)
        scroller.scrollTo({
          left: to,
          behavior: smooth && !reduceMotion ? "smooth" : "auto",
        });
    },
    [focusTurn, reduceMotion],
  );
  useLayoutEffect(() => {
    reveal(true);
    measure();
  }, [reveal, measure, turns.length]);

  // A narrower window must not hide the selection; the counts follow either way.
  useEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      reveal(false);
      measure();
    });
    ro.observe(scroller);
    return () => ro.disconnect();
  }, [reveal, measure]);

  // A vertical mouse wheel scrolls the strip sideways (trackpads already send
  // deltaX). React's onWheel is passive, so it can't preventDefault.
  useEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller) return;
    const onWheel = (e: WheelEvent) => {
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX) || scroller.scrollWidth <= scroller.clientWidth) return;
      e.preventDefault();
      scroller.scrollLeft += e.deltaY;
    };
    scroller.addEventListener("wheel", onWheel, { passive: false });
    return () => scroller.removeEventListener("wheel", onWheel);
  }, []);

  function click(e: MouseEvent, n: number) {
    if (!filter) {
      onNavigate(n);
      return;
    }
    onSelect(nextTurnSelection(sel, n, e.shiftKey, order));
  }

  function step(d: -1 | 1) {
    // Nothing chosen yet: start from what's in view (› its first turn, ‹ its
    // last) rather than jumping to a turn scrolled out of sight.
    const current = filter ? sel : spy;
    const edges = current === null && scrollerRef.current ? visibleEdges(scrollerRef.current) : null;
    if (edges) {
      const n = d > 0 ? edges[0] : edges[1];
      if (filter) onSelect([n]);
      else onNavigate(n);
      return;
    }
    if (!filter) {
      const n = stepTurn(spy, d, order);
      if (n !== null) onNavigate(n);
      return;
    }
    onSelect(stepTurnSelection(sel, d, order));
  }

  // Context line: the hovered turn previews; else the selection (or the turn in view).
  const show = hover ?? (sel ? sel[0] : filter ? null : spy);
  const shown = show !== null ? turnByN(turns, show) : undefined;
  const preview = hover !== null && !(sel ?? []).includes(hover);
  const range = sel && sel.length > 1 && hover === null ? `Turns ${sel[0]}–${sel[sel.length - 1]}` : `Turn ${show}`;

  return (
    <div className={styles.wrap}>
      <div className={styles.strip}>
        <div className={styles.side}>
          <button
            type="button"
            className={styles.arrow}
            disabled={lo !== null && lo === order[0]}
            onClick={() => step(-1)}
            title="Previous turn"
            aria-label="Previous turn"
          >
            ‹
          </button>
          <MoreCount n={hidden.left} />
        </div>
        <div
          className={styles.window}
          data-fade-l={hidden.left > 0 || undefined}
          data-fade-r={hidden.right > 0 || undefined}
        >
          <div className={styles.cols} ref={scrollerRef} onScroll={measure}>
            {turns.map((t) => {
              const isSel = !!sel?.includes(t.n);
              const isSpy = !filter && spy === t.n;
              return (
                <button
                  key={t.n}
                  type="button"
                  data-turn-col={t.n}
                  className={styles.col}
                  data-sel={isSel || undefined}
                  data-spy={isSpy || undefined}
                  aria-pressed={filter ? isSel : undefined}
                  aria-current={isSpy ? "true" : undefined}
                  aria-label={`Turn ${t.n}, ${t.files} ${t.files === 1 ? "file" : "files"}`}
                  onMouseEnter={() => setHover(t.n)}
                  onMouseLeave={() => setHover(null)}
                  onFocus={() => setHover(t.n)}
                  onBlur={() => setHover(null)}
                  onClick={(e) => click(e, t.n)}
                >
                  <span
                    className={styles.bar}
                    data-zero={t.files === 0 || undefined}
                    style={{ height: t.files ? 6 + (36 * t.files) / max : 2 }}
                  />
                  <span className={styles.n}>{t.n}</span>
                </button>
              );
            })}
          </div>
        </div>
        <div className={styles.side}>
          <button
            type="button"
            className={styles.arrow}
            disabled={hi !== null && hi === order[order.length - 1]}
            onClick={() => step(1)}
            title="Next turn"
            aria-label="Next turn"
          >
            ›
          </button>
          <MoreCount n={hidden.right} />
        </div>
        <div className={styles.ctx} data-preview={preview || undefined} data-testid="turn-context">
          {!shown ? (
            <>
              <span className={styles.head}>
                <span className={styles.tn}>All turns</span>
              </span>
              <span className={styles.none}>
                {filter
                  ? "Hover a turn to see its prompt. Click to filter to it, shift-click for a range."
                  : "Turn grouping: the strip follows your scroll."}
              </span>
            </>
          ) : (
            <>
              <span className={styles.head}>
                <span className={styles.tn}>{range}</span>
                <span className={styles.meta}>
                  {" · "}
                  {shown.files} {shown.files === 1 ? "file" : "files"}
                  {sel && hover === null && (
                    <>
                      {" · "}
                      <button type="button" className={styles.link} onClick={() => onSelect(null)}>
                        clear
                      </button>
                    </>
                  )}
                </span>
              </span>
              {/* Every turn has a prompt; an empty one (defensive) shows just "Turn N". */}
              {shown.prompt && (
                <span className={styles.prompt} title={shown.prompt}>
                  “{clipPrompt(shown.prompt)}”
                </span>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** "{n} more" under an arrow; holds its space when zero so the strip doesn't shift. */
function MoreCount({ n }: { n: number }) {
  return (
    <span className={styles.more} data-on={n > 0 || undefined} aria-hidden={n === 0 || undefined}>
      <b>{n}</b>more
    </span>
  );
}
