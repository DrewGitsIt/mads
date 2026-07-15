import { useState } from "react";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import styles from "./ToolCard.module.css";

export interface ToolCardProps {
  tool: ToolUse;
  defaultExpanded?: boolean;
}

function summarize(tool: ToolUse): string {
  if (tool.path) return tool.path;
  if (tool.input && typeof tool.input === "object") {
    const entries = Object.entries(tool.input as Record<string, unknown>);
    const first = entries[0];
    if (first) return `${first[0]}: ${String(first[1]).slice(0, 60)}`;
  }
  return "no input";
}

/** Collapsible tool-call card. Defaults collapsed; expand state is local, not persisted. */
export function ToolCard({ tool, defaultExpanded = false }: ToolCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);

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
        </div>
      )}
    </Card>
  );
}
