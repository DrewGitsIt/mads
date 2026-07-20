import { useState } from "react";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import styles from "./ToolCard.module.css";

export interface ToolCardProps {
  tool: ToolUse;
  /** When the tool_use itself was recorded — paired with tool.result.timestamp for the duration hint. */
  timestamp?: string;
  defaultExpanded?: boolean;
}

// Above this, the result body is capped with a "Show more" toggle rather
// than dumped in full — huge tool output (a big Bash/Grep result) would
// otherwise make the card unusable to scroll past.
const RESULT_CAP = 2000;

function summarize(tool: ToolUse): string {
  if (tool.path) return tool.path;
  if (tool.input && typeof tool.input === "object") {
    const entries = Object.entries(tool.input as Record<string, unknown>);
    const first = entries[0];
    if (first) return `${first[0]}: ${String(first[1]).slice(0, 60)}`;
  }
  return "no input";
}

function formatDuration(startIso?: string, endIso?: string): string | undefined {
  if (!startIso || !endIso) return undefined;
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return undefined;
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

/** Collapsible tool-call card. Defaults collapsed; expand state is local, not persisted. */
export function ToolCard({ tool, timestamp, defaultExpanded = false }: ToolCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const [resultExpanded, setResultExpanded] = useState(false);
  const result = tool.result;
  const duration = formatDuration(timestamp, result?.timestamp);

  return (
    <Card className={styles.card}>
      <button
        type="button"
        className={styles.header}
        aria-expanded={expanded}
        onClick={() => setExpanded((e) => !e)}
      >
        <Chip tone="info">{tool.name}</Chip>
        <span className={styles.summary}>{summarize(tool)}</span>
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
          {tool.input !== undefined && (
            <pre className={styles.input}>
              {JSON.stringify(tool.input, null, 2)}
            </pre>
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
