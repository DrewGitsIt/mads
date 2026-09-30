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
}: {
  marker: CompactMarker;
  summary?: string;
  timestamp?: string;
}) {
  const [open, setOpen] = useState(false);
  const at = parseMessageTime(timestamp);
  const details = compactDetails(marker);
  if (at) details.push(formatMessageTime(at));
  return (
    <div className={styles.marker} role="note" aria-label={compactTitle(marker)}>
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
              aria-expanded={open}
              onClick={() => setOpen((o) => !o)}
            >
              {open ? "Hide summary" : "Show summary"}
            </button>
          </>
        )}
      </div>
      {summary && open && <Markdown className={styles.summary} text={summary} />}
    </div>
  );
}
