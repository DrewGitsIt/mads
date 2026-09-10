import { useState, type ReactNode } from "react";
import type { DiffHunk } from "./toolDisplay";
import styles from "./DiffView.module.css";

// Diff hunks longer than this are truncated behind a "Show more" toggle so a
// whole-file Write doesn't blow up the surface hosting the diff.
const DIFF_LINE_CAP = 200;

/**
 * Color-coded stacked diff: removed lines then added lines, per hunk.
 * Shared by the bottom dock's detail view, row hover-peeks (capped), and
 * the pipeline drill-in's ToolCard.
 */
export function DiffView({ hunks, lineCap = DIFF_LINE_CAP }: { hunks: DiffHunk[]; lineCap?: number }) {
  const [expanded, setExpanded] = useState(false);
  const total = hunks.reduce((n, h) => n + h.removed.length + h.added.length, 0);
  let budget = expanded ? Infinity : lineCap;

  return (
    <div className={styles.diff}>
      {hunks.map((h, hi) => {
        const rows: ReactNode[] = [];
        for (const [i, l] of h.removed.entries()) {
          if (budget-- <= 0) break;
          rows.push(
            <div key={`d${i}`} className={styles.diffDel}>
              <span className={styles.diffGutter}>−</span>
              <span className={styles.diffText}>{l || " "}</span>
            </div>,
          );
        }
        for (const [i, l] of h.added.entries()) {
          if (budget-- <= 0) break;
          rows.push(
            <div key={`a${i}`} className={styles.diffAdd}>
              <span className={styles.diffGutter}>+</span>
              <span className={styles.diffText}>{l || " "}</span>
            </div>,
          );
        }
        return (
          <div key={hi} className={styles.diffHunk}>
            {rows}
          </div>
        );
      })}
      {total > lineCap && (
        <button type="button" className={styles.expand} onClick={() => setExpanded((e) => !e)}>
          {expanded ? "Show less" : `Show all ${total} lines`}
        </button>
      )}
    </div>
  );
}
