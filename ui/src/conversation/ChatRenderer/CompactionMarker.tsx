import { useState } from "react";
import type { CompactMarker } from "../../router/types";
import { Markdown } from "./markdown";
import { compactDetails, compactTitle } from "./compactFormat";
import { formatMessageTime, parseMessageTime } from "./messageTime";
import styles from "./CompactionMarker.module.css";

/**
 * A finished compaction as a system row in the history, where it happened
 * (docs/mockups/compaction-marker.html, "Done"). Built from the provider's
 * own record, so it survives a reload; the summary the agent carried
 * forward sits behind a disclosure rather than posing as a user message.
 */
export function CompactionMarker({
  marker,
  summary,
  timestamp,
  findKey,
  findMatches = 0,
  findOpen = false,
}: {
  marker: CompactMarker;
  summary?: string;
  timestamp?: string;
  /** This marker's item key, for find (useFind's `data-find-summary`). */
  findKey?: string;
  /** Find's matches in the summary. */
  findMatches?: number;
  /** Find's current match is in the summary: it shows, on top of the user's own toggle. */
  findOpen?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const shown = open || findOpen;
  const at = parseMessageTime(timestamp);
  const details = compactDetails(marker);
  if (at) details.push(formatMessageTime(at));
  return (
    <div className={styles.marker} role="note" aria-label={compactTitle(marker)} data-find-summary={findKey}>
      <div className={styles.line}>
        <span className={styles.glyph} aria-hidden="true">
          ⇲
        </span>
        <span>{compactTitle(marker)}</span>
        {details.length > 0 && (
          <>
            <span className={styles.sep} aria-hidden="true">
              ·
            </span>
            <span className={styles.meta} title={at?.toLocaleString()}>
              {details.join(" · ")}
            </span>
          </>
        )}
        {summary && (
          <>
            <span className={styles.sep} aria-hidden="true">
              ·
            </span>
            <button
              type="button"
              className={styles.disclose}
              aria-expanded={shown}
              onClick={() => setOpen(!shown)}
            >
              {shown ? "Hide summary" : "Show summary"}
            </button>
            {!shown && findMatches > 0 && (
              <span className={styles.findHits}>
                {findMatches} {findMatches === 1 ? "match" : "matches"}
              </span>
            )}
          </>
        )}
      </div>
      {summary && shown && (
        <div className={styles.summaryBody} data-find-summary-body>
          <Markdown className={styles.summary} text={summary} />
        </div>
      )}
    </div>
  );
}
