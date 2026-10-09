import { useEffect, useMemo, useRef, useState } from "react";
import type { PermissionRequest, ToolDiff, ToolStatus, ToolUse } from "../../router/types";
import { ActivityGroup } from "./ActivityGroup";
import type { ActivityEntry, SubStreamItem, TaskModel } from "./conversationItems";
import { DiffView } from "./DiffView";
import { KindIcon, McpBadge, Spinner, StatusGlyph } from "./primitives";
import { TaskCounters, TaskStatusGlyph } from "./TaskCard";
import {
  commandInput,
  diffHunks,
  formatDuration,
  kindOf,
  mcpServerOf,
  resultLabel,
  serverDiffHunk,
  statusOf,
  toolTitle,
} from "./toolDisplay";
import styles from "./ActivityDock.module.css";

/** What the dock is showing: one tool call's full detail, or a subagent's stream. */
export type DockView =
  | { type: "tool"; tool: ToolUse; timestamp?: string }
  | {
      type: "subagent";
      parentToolUseId: string;
      title: string;
      subtype?: string;
      items: SubStreamItem[];
      task?: TaskModel;
    };

export interface ActivityDockProps {
  view: DockView;
  sessionId: string;
  /** Only the tool-diff read is needed — narrow so tests can stub a two-line object. */
  client: { getToolDiff(sessionId: string, toolUseId: string): Promise<ToolDiff | null> };
  statuses?: ReadonlyMap<string, ToolStatus>;
  /** Permission requests by tool_use id — a subagent call's card renders under its row in the stream view. */
  permissions?: ReadonlyMap<string, PermissionRequest[]>;
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
  onClose: () => void;
  /** Opens the dock's subagent as a full-pane view (a delegate); absent hides the control. */
  onOpenPage?: () => void;
}

/**
 * Bottom dock (T13, mockup surface D): a centered drawer, sized relative
 * to the conversation pane (92% of .tab, its positioning context), with
 * rounded top shoulders sliding up over the stream. Hosts full tool
 * detail — input, output, and the historically-accurate diff from
 * GET /sessions/{id}/tool-diff (404 = keep the input-derived diff) — and
 * subagent streams, rendered with the same grouping components as the main
 * pane. Esc and outside-click close; clicking another row retargets.
 */
export function ActivityDock({
  view,
  sessionId,
  client,
  statuses,
  permissions,
  onResolvePermission,
  onClose,
  onOpenPage,
}: ActivityDockProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const onDown = (e: MouseEvent) => {
      const target = e.target as Element | null;
      // Clicks on the dock itself, or on any element that (re)targets the
      // dock (rows, task cards, the substrip), never count as "outside" —
      // nor do permission cards and the approval jump: deciding a subagent's
      // request from its main-stream card shouldn't close the stream it's in.
      const inside =
        "[data-activity-dock],[data-dock-source],[data-permission-id],[data-permission-jump]";
      if (target?.closest?.(inside)) return;
      onClose();
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [onClose]);

  return (
    <div className={styles.dock} data-activity-dock ref={ref} role="dialog" aria-label="Activity detail">
      {view.type === "tool" ? (
        <ToolDetail
          tool={view.tool}
          timestamp={view.timestamp}
          sessionId={sessionId}
          client={client}
          statuses={statuses}
          onClose={onClose}
        />
      ) : (
        <SubagentDetail
          view={view}
          statuses={statuses}
          permissions={permissions}
          onResolvePermission={onResolvePermission}
          onClose={onClose}
          onOpenPage={onOpenPage}
        />
      )}
    </div>
  );
}

type DiffState =
  | { state: "idle" | "loading" }
  | { state: "server"; diff: ToolDiff }
  | { state: "fallback" };

function ToolDetail({
  tool,
  timestamp,
  sessionId,
  client,
  statuses,
  onClose,
}: {
  tool: ToolUse;
  timestamp?: string;
  sessionId: string;
  client: ActivityDockProps["client"];
  statuses?: ReadonlyMap<string, ToolStatus>;
  onClose: () => void;
}) {
  const kind = kindOf(tool);
  const server = mcpServerOf(tool);
  const status = statusOf(tool, tool.id ? statuses?.get(tool.id) : undefined, false);
  const duration = formatDuration(timestamp, tool.result?.timestamp);
  const inputHunks = useMemo(() => diffHunks(tool), [tool]);
  const cmd = commandInput(tool);

  // Historical diff: only meaningful for mutating (edit/delete-kind) calls
  // with an id. 404 (null) is the expected "no shadow capture" answer —
  // keep the input-derived diff, exactly today's behavior.
  const [diff, setDiff] = useState<DiffState>({ state: "idle" });
  const wantsServerDiff = Boolean(tool.id && inputHunks);
  useEffect(() => {
    if (!wantsServerDiff || !tool.id) {
      setDiff({ state: "idle" });
      return;
    }
    let cancelled = false;
    setDiff({ state: "loading" });
    client
      .getToolDiff(sessionId, tool.id)
      .then((d) => {
        if (!cancelled) setDiff(d ? { state: "server", diff: d } : { state: "fallback" });
      })
      .catch(() => {
        if (!cancelled) setDiff({ state: "fallback" });
      });
    return () => {
      cancelled = true;
    };
  }, [client, sessionId, tool.id, wantsServerDiff]);

  const result = tool.result;
  return (
    <>
      <div className={styles.head}>
        <KindIcon kind={kind} />
        <span className={styles.title}>{toolTitle(tool)}</span>
        {server && <McpBadge server={server} />}
        <span className={styles.meta}>
          {duration && <span>{duration}</span>}
          {status === "in_progress" ? <Spinner /> : <StatusGlyph status={status} />}
        </span>
        <button type="button" className={styles.close} aria-label="Close detail" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className={styles.body}>
        {inputHunks ? (
          <ToolDiffSection tool={tool} inputHunks={inputHunks} diff={diff} />
        ) : cmd ? (
          <>
            <div className={styles.lbl}>{tool.name} · command</div>
            <pre className={styles.pre}>{cmd.command}</pre>
            {cmd.options && (
              <>
                <div className={styles.lbl}>options</div>
                <pre className={styles.pre}>{JSON.stringify(cmd.options, null, 2)}</pre>
              </>
            )}
          </>
        ) : (
          tool.input !== undefined && (
            <>
              <div className={styles.lbl}>{tool.name} · input</div>
              <pre className={styles.pre}>{JSON.stringify(tool.input, null, 2)}</pre>
            </>
          )
        )}
        {result && (
          <div className={result.is_error ? styles.error : undefined}>
            <div className={styles.lbl}>{resultLabel(result, status)}</div>
            <pre className={styles.pre}>{result.text ?? ""}</pre>
          </div>
        )}
        {!result && status === "interrupted" && (
          <>
            <div className={styles.lbl}>interrupted</div>
            <pre className={styles.pre}>
              turn interrupted before the result arrived — no output was recorded
            </pre>
          </>
        )}
      </div>
    </>
  );
}

function ToolDiffSection({
  tool,
  inputHunks,
  diff,
}: {
  tool: ToolUse;
  inputHunks: NonNullable<ReturnType<typeof diffHunks>>;
  diff: DiffState;
}) {
  if (diff.state === "server") {
    const d = diff.diff;
    const bothTooLarge = d.original_too_large && d.modified_too_large;
    return (
      <>
        <div className={styles.lbl}>
          {tool.name} · {d.path} · recorded at execution time
        </div>
        {d.original_too_large && (
          <div className={styles.note} data-testid="diff-too-large">
            The file's before-image exceeded the capture limit — the original side is unavailable.
          </div>
        )}
        {d.modified_too_large && (
          <div className={styles.note} data-testid="diff-too-large">
            The file's after-image exceeded the capture limit — the modified side is unavailable.
          </div>
        )}
        {bothTooLarge ? (
          <DiffView hunks={inputHunks} />
        ) : (
          <DiffView hunks={[serverDiffHunk(d)]} />
        )}
      </>
    );
  }
  return (
    <>
      <div className={styles.lbl}>
        {tool.name}
        {tool.path ? ` · ${tool.path}` : ""}
        {diff.state === "loading" ? " · checking recorded diff…" : ""}
      </div>
      <DiffView hunks={inputHunks} />
    </>
  );
}

function SubagentDetail({
  view,
  statuses,
  permissions,
  onResolvePermission,
  onClose,
  onOpenPage,
}: {
  view: Extract<DockView, { type: "subagent" }>;
  statuses?: ReadonlyMap<string, ToolStatus>;
  permissions?: ReadonlyMap<string, PermissionRequest[]>;
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
  onClose: () => void;
  onOpenPage?: () => void;
}) {
  const blocked = statuses?.get(view.parentToolUseId) === "blocked";
  const running = !blocked && (view.task?.running ?? false);
  return (
    <>
      <div className={styles.head}>
        <KindIcon kind="agent" />
        <span className={styles.title}>{view.title}</span>
        <span className={styles.meta}>
          {view.subtype && <span>{view.subtype}</span>}
          {view.task && (
            <span>
              <TaskCounters model={view.task} />
            </span>
          )}
          {blocked || view.task?.state ? (
            <TaskStatusGlyph model={view.task} blocked={blocked} />
          ) : running ? (
            <Spinner />
          ) : view.task?.completed ? (
            <StatusGlyph status="completed" />
          ) : view.task ? (
            <StatusGlyph status="interrupted" />
          ) : null}
        </span>
        {onOpenPage && (
          <button type="button" className={styles.openPage} onClick={onOpenPage} title="Open this subagent as a page">
            Open as page ↗
          </button>
        )}
        <button type="button" className={styles.close} aria-label="Close detail" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className={styles.body} data-testid="subagent-stream">
        <SubagentStream
          items={view.items}
          running={running}
          summary={view.task?.summary}
          statuses={statuses}
          permissions={permissions}
          onResolvePermission={onResolvePermission}
        />
      </div>
    </>
  );
}

/**
 * A subagent's own stream — text paragraphs between groups of tool/thinking
 * rows, folded with the same components as the main conversation. Shared by
 * the dock and the full-pane delegate view, so a delegate reads the same in
 * both. The newest group is live while the subagent runs.
 */
export function SubagentStream({
  items,
  running,
  summary,
  statuses,
  permissions,
  onResolvePermission,
}: {
  items: SubStreamItem[];
  running: boolean;
  summary?: string;
  statuses?: ReadonlyMap<string, ToolStatus>;
  permissions?: ReadonlyMap<string, PermissionRequest[]>;
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
}) {
  // Same grouping components as the main stream: fold contiguous
  // tool/thinking items into groups between text paragraphs.
  const blocks = useMemo(() => {
    const out: ({ type: "text"; text: string } | { type: "group"; entries: ActivityEntry[] })[] = [];
    for (const item of items) {
      if (item.type === "text") {
        out.push(item);
        continue;
      }
      const last = out[out.length - 1];
      if (last && last.type === "group") last.entries.push(item);
      else out.push({ type: "group", entries: [item] });
    }
    return out;
  }, [items]);

  return (
    <>
      {items.length === 0 && <div className={styles.subText}>No subagent activity recorded yet.</div>}
      {blocks.map((b, i) =>
        b.type === "text" ? (
          <div key={i} className={styles.subText}>
            {b.text}
          </div>
        ) : (
          <div key={i} className={styles.subGroup}>
            <ActivityGroup
              entries={b.entries}
              defaultOpen
              live={running && i === blocks.length - 1}
              statuses={statuses}
              permissions={permissions}
              onResolvePermission={onResolvePermission}
            />
          </div>
        ),
      )}
      {summary && <div className={styles.subText}>{summary}</div>}
    </>
  );
}
