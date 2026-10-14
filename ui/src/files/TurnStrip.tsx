import { useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import type { ChangeTurn } from "../router/types";
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

/** Past this many turns the columns narrow and only every fifth carries its number. */
const DENSE_AT = 30;

/**
 * The turn strip (docs/mockups/changes-v2.html): one column per user turn,
 * bar height = files that turn touched, a flat stub for zero-file turns. The
 * whole column is the hit target. Hovering previews a turn's prompt on the
 * context line below; clicking pins it.
 */
export function TurnStrip({ turns, mode, selected, onSelect, spy, onNavigate }: TurnStripProps) {
  const [hover, setHover] = useState<number | null>(null);
  const scrollerRef = useRef<HTMLDivElement>(null);
  const order = turns.map((t) => t.n);
  const max = Math.max(1, ...turns.map((t) => t.files));
  const filter = mode === "filter";
  const sel = filter ? selected : null;
  const current = filter ? sel : spy !== null ? [spy] : null;
  const lo = current?.[0] ?? null;
  const hi = current ? current[current.length - 1] : null;
  const dense = turns.length > DENSE_AT;

  // Long sessions scroll horizontally: keep the selected / in-view turn visible.
  const focusTurn = filter ? (sel ? sel[sel.length - 1] : null) : spy;
  useLayoutEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller || focusTurn === null) return;
    const col = scroller.querySelector<HTMLElement>(`[data-turn-col="${focusTurn}"]`);
    if (!col) return;
    const left = col.offsetLeft;
    const right = left + col.offsetWidth;
    if (left < scroller.scrollLeft) scroller.scrollLeft = left - 4;
    else if (right > scroller.scrollLeft + scroller.clientWidth) scroller.scrollLeft = right - scroller.clientWidth + 4;
  }, [focusTurn]);

  function click(e: MouseEvent, n: number) {
    if (!filter) {
      onNavigate(n);
      return;
    }
    onSelect(nextTurnSelection(sel, n, e.shiftKey, order));
  }

  function step(d: -1 | 1) {
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
        <div className={styles.cols} ref={scrollerRef} data-dense={dense || undefined}>
          {turns.map((t) => {
            const isSel = !!sel?.includes(t.n);
            const isSpy = !filter && spy === t.n;
            const label = dense && t.n % 5 !== 0 && !isSel && !isSpy ? "" : String(t.n);
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
                <span className={styles.n}>{label}</span>
              </button>
            );
          })}
        </div>
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
        <span className={styles.hint}>
          {filter ? "Click to filter · shift-click for a range" : "Turn grouping: the strip follows your scroll"}
        </span>
      </div>
      <div className={styles.ctx} data-preview={preview || undefined} data-testid="turn-context">
        {!shown ? (
          <>
            <span className={styles.tn}>All turns</span>
            <span className={styles.none}>Hover a turn to see its prompt; click to filter to it.</span>
          </>
        ) : (
          <>
            <span className={styles.tn}>{range}</span>
            {/* Every turn has a prompt; an empty one (defensive) shows just "Turn N". */}
            {shown.prompt && (
              <span className={styles.prompt} title={shown.prompt}>
                “{clipPrompt(shown.prompt)}”
              </span>
            )}
            <span className={styles.spacer} />
            <span className={styles.meta}>
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
          </>
        )}
      </div>
    </div>
  );
}
