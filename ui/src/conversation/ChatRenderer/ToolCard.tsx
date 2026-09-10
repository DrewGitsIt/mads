import { useState } from "react";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import { DiffView } from "../activity/DiffView";
import { diffHunks, diffStat, formatDuration, toolTitle } from "../activity/toolDisplay";
import styles from "./ToolCard.module.css";

export interface ToolCardProps {
  tool: ToolUse;
  /** When the tool_use itself was recorded — paired with tool.result.timestamp for the duration hint. */
  timestamp?: string;
  defaultExpanded?: boolean;
  /**
   * Rendered from an in-flight stream rather than a persisted transcript.
   * Live cards ease in instead of blipping, and show a running spinner
   * until their result attaches — historical cards (and any transcript
   * re-render) stay static, so a refetch never replays entrance motion.
   */
  live?: boolean;
}

// Above this, the result body is capped with a "Show more" toggle rather
// than dumped in full — huge tool output (a big Bash/Grep result) would
// otherwise make the card unusable to scroll past.
const RESULT_CAP = 2000;

/**
 * Collapsible tool-call card. The conversation pane replaced this with the
 * grouped-activity system + bottom dock (ui/src/conversation/activity/);
 * this card remains the pipeline drill-in feed's tool row
 * (ui/src/pipeline/DrillIn/AgentFeedView.tsx). Title/diff derivations are
 * shared with the new system via activity/toolDisplay.ts — the server-built
 * `title` wins, summarize() is only the legacy fallback.
 */
export function ToolCard({ tool, timestamp, defaultExpanded = false, live = false }: ToolCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const [resultExpanded, setResultExpanded] = useState(false);
  const result = tool.result;
  const duration = formatDuration(timestamp, result?.timestamp);
  const hunks = diffHunks(tool);
  const stat = hunks ? diffStat(hunks) : undefined;

  return (
    <Card className={live ? [styles.card, styles.cardEnter].join(" ") : styles.card}>
      <button
        type="button"
        className={styles.header}
        aria-expanded={expanded}
        onClick={() => setExpanded((e) => !e)}
      >
        <Chip tone="info">{tool.name}</Chip>
        <span className={styles.summary}>{toolTitle(tool)}</span>
        {stat && (stat.adds > 0 || stat.dels > 0) && (
          <span className={styles.diffStat}>
            {stat.adds > 0 && <span className={styles.diffStatAdd}>+{stat.adds}</span>}
            {stat.dels > 0 && <span className={styles.diffStatDel}>−{stat.dels}</span>}
          </span>
        )}
        {live && !result && (
          <span className={styles.status}>
            <span className={styles.spinner} aria-hidden="true" />
            <span className={styles.running} role="status">
              running
            </span>
          </span>
        )}
        {result && (
          <span className={styles.status}>
            <Chip tone={result.is_error ? "bad" : "ok"}>{result.is_error ? "error" : "ok"}</Chip>
            {duration && <span className={styles.duration}>{duration}</span>}
          </span>
        )}
        <span className={styles.chevron} data-expanded={expanded}>
          ▸
        </span>
      </button>
      {expanded && (
        <div className={styles.body}>
          {tool.path && <div className={styles.path}>{tool.path}</div>}
          {hunks ? (
            <DiffView hunks={hunks} />
          ) : (
            tool.input !== undefined && (
              <pre className={styles.input}>{JSON.stringify(tool.input, null, 2)}</pre>
            )
          )}
          {result && (
            <div className={styles.result} data-error={result.is_error || undefined}>
              <div className={styles.resultLabel}>{result.is_error ? "error" : "result"}</div>
              <pre className={styles.resultText}>
                {resultExpanded || (result.text?.length ?? 0) <= RESULT_CAP
                  ? result.text
                  : `${result.text!.slice(0, RESULT_CAP)}…`}
              </pre>
              {(result.text?.length ?? 0) > RESULT_CAP && (
                <button
                  type="button"
                  className={styles.resultExpand}
                  onClick={() => setResultExpanded((e) => !e)}
                >
                  {resultExpanded ? "Show less" : "Show more"}
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
