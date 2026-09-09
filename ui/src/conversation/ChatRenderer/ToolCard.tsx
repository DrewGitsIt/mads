import { useState, type ReactNode } from "react";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import styles from "./ToolCard.module.css";

export interface ToolCardProps {
  tool: ToolUse;
  /** When the tool_use itself was recorded — paired with tool.result.timestamp for the duration hint. */
  timestamp?: string;
  defaultExpanded?: boolean;
  /**
   * Rendered from the in-flight turn's stream (ChatRenderer liveItems)
   * rather than the persisted transcript. Live cards ease in instead of
   * blipping, and show a running spinner until their result attaches —
   * historical cards (and the transcript re-render on turn_end) stay
   * static, so a refetch never replays entrance motion.
   */
  live?: boolean;
}

// Above this, the result body is capped with a "Show more" toggle rather
// than dumped in full — huge tool output (a big Bash/Grep result) would
// otherwise make the card unusable to scroll past.
const RESULT_CAP = 2000;

// Diff hunks longer than this are truncated behind a "Show more" toggle so a
// whole-file Write doesn't blow up the stream.
const DIFF_LINE_CAP = 200;

interface DiffHunk {
  removed: string[];
  added: string[];
}

/**
 * A file-mutating tool (Write/Edit/MultiEdit/NotebookEdit) carries its change
 * as before/after text in the tool input — turn that into diff hunks so the
 * card can render a color-coded stacked diff instead of raw JSON. Returns null
 * for every other tool (rendered as JSON input as before).
 */
function diffHunks(tool: ToolUse): DiffHunk[] | null {
  if (!tool.input || typeof tool.input !== "object") return null;
  const o = tool.input as Record<string, unknown>;
  const lines = (v: unknown) => (typeof v === "string" ? v.split("\n") : []);

  switch (tool.name) {
    case "Write":
      return typeof o.content === "string" ? [{ removed: [], added: lines(o.content) }] : null;
    case "NotebookEdit":
      return typeof o.new_source === "string" ? [{ removed: [], added: lines(o.new_source) }] : null;
    case "Edit":
      if (typeof o.old_string !== "string" && typeof o.new_string !== "string") return null;
      return [{ removed: lines(o.old_string), added: lines(o.new_string) }];
    case "MultiEdit":
      if (!Array.isArray(o.edits)) return null;
      return (o.edits as Array<Record<string, unknown>>).map((e) => ({
        removed: lines(e.old_string),
        added: lines(e.new_string),
      }));
    default:
      return null;
  }
}

function diffStat(hunks: DiffHunk[]): { adds: number; dels: number } {
  return hunks.reduce(
    (acc, h) => ({ adds: acc.adds + h.added.length, dels: acc.dels + h.removed.length }),
    { adds: 0, dels: 0 },
  );
}

/**
 * One line of the most relevant input for the header, per tool: what the
 * command does (Bash), what's being searched for and where (Grep/Glob), the
 * delegated task (Task), the target (WebFetch/WebSearch), the file (anything
 * with a path). Falls back to the first input key only for tools with no
 * better-known shape.
 */
function summarize(tool: ToolUse): string {
  const input =
    tool.input && typeof tool.input === "object" ? (tool.input as Record<string, unknown>) : {};
  const str = (v: unknown) => (typeof v === "string" && v.trim() !== "" ? v : undefined);
  switch (tool.name) {
    case "Bash":
      return str(input.description) ?? str(input.command) ?? "no input";
    case "Grep":
    case "Glob": {
      const pattern = str(input.pattern);
      if (pattern) {
        const where = str(input.path);
        return where ? `${pattern} in ${where}` : pattern;
      }
      break;
    }
    case "Task":
      if (str(input.description)) return input.description as string;
      break;
    case "WebFetch":
      if (str(input.url)) return input.url as string;
      break;
    case "WebSearch":
      if (str(input.query)) return input.query as string;
      break;
  }
  if (tool.path) return tool.path;
  const first = Object.entries(input)[0];
  if (first) return `${first[0]}: ${String(first[1]).slice(0, 60)}`;
  return "no input";
}

function formatDuration(startIso?: string, endIso?: string): string | undefined {
  if (!startIso || !endIso) return undefined;
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return undefined;
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

/** Collapsible tool-call card. Defaults collapsed; expand state is local, not persisted. */
export function ToolCard({ tool, timestamp, defaultExpanded = false, live = false }: ToolCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const [resultExpanded, setResultExpanded] = useState(false);
  const [diffExpanded, setDiffExpanded] = useState(false);
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
        <span className={styles.summary}>{summarize(tool)}</span>
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
            <DiffView hunks={hunks} expanded={diffExpanded} onToggle={() => setDiffExpanded((e) => !e)} />
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

/** Color-coded stacked diff: removed lines then added lines, per hunk. */
function DiffView({
  hunks,
  expanded,
  onToggle,
}: {
  hunks: DiffHunk[];
  expanded: boolean;
  onToggle: () => void;
}) {
  const total = hunks.reduce((n, h) => n + h.removed.length + h.added.length, 0);
  let budget = expanded ? Infinity : DIFF_LINE_CAP;

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
      {total > DIFF_LINE_CAP && (
        <button type="button" className={styles.resultExpand} onClick={onToggle}>
          {expanded ? "Show less" : `Show all ${total} lines`}
        </button>
      )}
    </div>
  );
}
