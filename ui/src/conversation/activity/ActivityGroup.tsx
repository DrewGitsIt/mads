import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { PermissionRequest, ToolKind, ToolStatus, ToolUse } from "../../router/types";
import { PermissionCard } from "../ChatRenderer/PermissionCard";
import type { ActivityEntry, ToolEntry } from "./conversationItems";
import { DiffView } from "./DiffView";
import { Diffstat, KindIcon, McpBadge, Spinner, StatusGlyph } from "./primitives";
import {
  basename,
  commandInput,
  diffHunks,
  diffStat,
  formatDuration,
  kindOf,
  mcpServerOf,
  primaryLocation,
  resultLabel,
  statusOf,
  toolTitle,
} from "./toolDisplay";
import styles from "./ActivityGroup.module.css";

export interface ActivityGroupProps {
  entries: ActivityEntry[];
  /**
   * This group belongs to the in-flight turn: it renders open, grows in
   * place, and its new rows ease in. When the turn ends (live flips false)
   * the group collapses itself — the replay/transcript re-render never
   * animates (same contract ToolCard's `live` prop kept).
   */
  live?: boolean;
  /** Out-of-band status overrides (session.tool_status_changed), by tool_use id. */
  statuses?: ReadonlyMap<string, ToolStatus>;
  /** Row whose detail is open in the dock — suppresses that row's hover-peek. */
  selectedToolId?: string;
  /** Row click → open/retarget the dock. Absent = rows are inert (subagent dock view). */
  onOpenTool?: (tool: ToolUse) => void;
  /** Open on mount regardless of liveness (dock's subagent stream view). */
  defaultOpen?: boolean;
  /** Permission requests anchored to a call, by tool_use id — each renders right under its row. */
  permissions?: ReadonlyMap<string, PermissionRequest[]>;
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
}

const PEEK_TEXT_CAP = 400;
const PEEK_DIFF_LINES = 8;
const noResolve = async () => {};

function entryStatus(entry: ActivityEntry, statuses?: ReadonlyMap<string, ToolStatus>, live?: boolean): ToolStatus | undefined {
  if (entry.type !== "tool") return undefined;
  const override = entry.tool.id ? statuses?.get(entry.tool.id) : undefined;
  return statusOf(entry.tool, override, live && entry.live);
}

/**
 * Aggregate status for the header: running beats failed beats blocked beats
 * pending beats interrupted beats ✓ — blocked is "something went wrong" like
 * failed, but a real failure is the louder story when the group has both,
 * so blocked shows only when it's the worst. Only the calls decide — a live
 * group whose calls have all resolved is done, even while the turn runs on
 * (the working indicator covers the turn); spinning on `live` alone kept
 * every finished group of a long turn spinning until its end.
 */
function aggregateStatus(entries: ActivityEntry[], statuses?: ReadonlyMap<string, ToolStatus>, live?: boolean): ToolStatus {
  const all = entries
    .map((e) => entryStatus(e, statuses, live))
    .filter((s): s is ToolStatus => s !== undefined);
  if (all.includes("in_progress")) return "in_progress";
  if (all.includes("failed")) return "failed";
  if (all.includes("blocked")) return "blocked";
  if (all.includes("pending")) return "pending";
  if (all.includes("interrupted")) return "interrupted";
  return "completed";
}

function kindCounts(entries: ActivityEntry[]): { kinds: ToolKind[]; label: string } {
  const counts = new Map<ToolKind, number>();
  for (const e of entries) {
    const k = e.type === "tool" ? kindOf(e.tool) : "think";
    counts.set(k, (counts.get(k) ?? 0) + 1);
  }
  const n = entries.length;
  const parts = [...counts.entries()].map(([k, c]) => `${c} ${k}`);
  return {
    kinds: [...counts.keys()].filter((k) => k !== "think"),
    label: `${n} action${n === 1 ? "" : "s"}${parts.length > 0 ? ` · ${parts.join(" · ")}` : ""}`,
  };
}

// The group's span (first start → last end) — but only once EVERY call in
// the group has both stamps. A partial span lies: a group mixing one
// resolved refetched call with unstamped live ones would report just that
// first call's duration as the whole group's.
function groupDuration(entries: ActivityEntry[]): string | undefined {
  let start: string | undefined;
  let end: string | undefined;
  let tools = 0;
  for (const e of entries) {
    if (e.type !== "tool") continue;
    tools++;
    const s = e.timestamp;
    const t = e.tool.result?.timestamp;
    if (!s || !t) return undefined;
    if (!start || s < start) start = s;
    if (!end || t > end) end = t;
  }
  if (tools === 0) return undefined;
  return formatDuration(start, end);
}

/**
 * Always-grouped activity run (T12): collapsed one-line header (kind set,
 * counts, duration, aggregate status); expanded, one 13.5px row per call
 * with a start-truncated title, location basename, diffstat and status
 * glyph; hover-peek on every row.
 */
export function ActivityGroup({
  entries,
  live = false,
  statuses,
  selectedToolId,
  onOpenTool,
  defaultOpen = false,
  permissions,
  onResolvePermission,
}: ActivityGroupProps) {
  const [open, setOpen] = useState(live || defaultOpen);
  // Collapse exactly once when the live turn ends — never re-collapse a
  // group the user has since reopened.
  const wasLive = useRef(live);
  useEffect(() => {
    if (wasLive.current && !live) setOpen(false);
    if (live) setOpen(true);
    wasLive.current = live;
  }, [live]);

  // A pending request must never hide inside a collapsed group: each newly
  // pending one opens the group, and if the user collapses it again anyway
  // the header carries a "needs your approval" chip until it's decided.
  const awaiting = entries
    .flatMap((e) => (e.type === "tool" && e.tool.id ? (permissions?.get(e.tool.id) ?? []) : []))
    .filter((r) => r.status === "pending")
    .map((r) => r.id)
    .join(",");
  useEffect(() => {
    if (awaiting) setOpen(true);
  }, [awaiting]);

  const { kinds, label } = kindCounts(entries);
  const duration = useMemo(() => groupDuration(entries), [entries]);
  const agg = aggregateStatus(entries, statuses, live);
  const stat = useMemo(() => {
    const totals = { adds: 0, dels: 0 };
    for (const e of entries) {
      if (e.type !== "tool") continue;
      const hunks = diffHunks(e.tool);
      if (!hunks) continue;
      const s = diffStat(hunks);
      totals.adds += s.adds;
      totals.dels += s.dels;
    }
    return totals;
  }, [entries]);

  return (
    <div className={styles.group} data-open={open || undefined} data-testid="activity-group">
      <button
        type="button"
        className={styles.head}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={styles.chevron} aria-hidden="true">
          ▶
        </span>
        <span className={styles.kinds}>
          {kinds.map((k) => (
            <KindIcon key={k} kind={k} />
          ))}
        </span>
        <span>{label}</span>
        <span className={styles.meta}>
          {!open && awaiting && <Chip tone="warn">needs your approval</Chip>}
          <Diffstat adds={stat.adds} dels={stat.dels} />
          {duration && <span>{duration}</span>}
          {agg === "in_progress" ? <Spinner /> : <StatusGlyph status={agg} />}
        </span>
      </button>
      {open && (
        <div className={styles.body}>
          {entries.map((entry, i) => {
            const requests =
              entry.type === "tool" && entry.tool.id ? permissions?.get(entry.tool.id) : undefined;
            return (
              <Fragment key={entry.type === "tool" ? (entry.tool.id ?? `t${i}`) : `th${i}`}>
                <ActivityRow
                  entry={entry}
                  status={entryStatus(entry, statuses, live)}
                  selected={
                    entry.type === "tool" &&
                    entry.tool.id !== undefined &&
                    entry.tool.id === selectedToolId
                  }
                  onOpen={onOpenTool}
                />
                {requests?.map((req) => (
                  <div key={req.id} className={styles.permission}>
                    <PermissionCard request={req} onResolve={onResolvePermission ?? noResolve} />
                  </div>
                ))}
              </Fragment>
            );
          })}
        </div>
      )}
    </div>
  );
}

function firstLine(text: string, cap = 100): string {
  const line = text.split("\n", 1)[0];
  return line.length > cap ? `${line.slice(0, cap)}…` : line;
}

export function ActivityRow({
  entry,
  status,
  selected,
  onOpen,
}: {
  entry: ActivityEntry;
  status?: ToolStatus;
  selected?: boolean;
  onOpen?: (tool: ToolUse) => void;
}) {
  const [hovered, setHovered] = useState(false);
  const isTool = entry.type === "tool";
  const tool = isTool ? (entry as ToolEntry).tool : undefined;
  const kind = tool ? kindOf(tool) : "think";
  const title = tool ? toolTitle(tool) : `Thought · ${firstLine(entry.type === "thinking" ? entry.text : "")}`;
  const location = tool ? primaryLocation(tool) : undefined;
  const server = tool ? mcpServerOf(tool) : undefined;
  const hunks = tool ? diffHunks(tool) : null;
  const stat = hunks ? diffStat(hunks) : undefined;
  const duration = tool ? formatDuration(entry.timestamp, tool.result?.timestamp) : undefined;
  const clickable = Boolean(onOpen && tool);
  // Peek suppression: never while this row's detail is open in the dock.
  const peekVisible = hovered && !selected;

  const line = (
    <>
      <KindIcon kind={kind} />
      {/* Unknown tools get a name prefix — their fallback titles are bare
          input previews ("stop: true") that say nothing about the actor. */}
      {kind === "other" && tool?.name && <span className={styles.rowName}>{tool.name}</span>}
      <span className={styles.rowTitle} title={title}>
        <bdi>{title}</bdi>
      </span>
      {server && <McpBadge server={server} />}
      {location && !title.includes(basename(location)) && (
        <span className={styles.rowLoc}>{basename(location)}</span>
      )}
      <span className={styles.rowMeta}>
        {stat && <Diffstat adds={stat.adds} dels={stat.dels} />}
        {duration && <span>{duration}</span>}
        {status &&
          (status === "in_progress" ? <Spinner /> : <StatusGlyph status={status} />)}
      </span>
    </>
  );

  const rowCls = [
    styles.row,
    entry.type === "thinking" ? styles.think : "",
    entry.live ? styles.rowEnter : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div
      className={rowCls}
      data-sel={selected || undefined}
      data-testid="activity-row"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {clickable ? (
        <button
          type="button"
          className={styles.rowLine}
          data-dock-source
          onClick={() => onOpen!(tool!)}
        >
          {line}
        </button>
      ) : (
        <div className={styles.rowLine}>{line}</div>
      )}
      {peekVisible && <Peek entry={entry} status={status} clickable={clickable} />}
    </div>
  );
}

function inputPreview(tool: ToolUse): string | undefined {
  if (!tool.input) return undefined;
  // A bash call previews as its bare command — the description already is
  // the row title, and key: value lines just repeat it around the command.
  const cmd = commandInput(tool);
  if (cmd) return cmd.command.slice(0, PEEK_TEXT_CAP);
  if (typeof tool.input !== "object") return String(tool.input).slice(0, PEEK_TEXT_CAP);
  const lines = Object.entries(tool.input as Record<string, unknown>)
    .slice(0, 4)
    .map(([k, v]) => {
      const s = typeof v === "string" ? v : JSON.stringify(v);
      return `${k}: ${s == null ? "" : s.length > 120 ? `${s.slice(0, 120)}…` : s}`;
    });
  return lines.join("\n") || undefined;
}

/**
 * Flip-up detection: a peek opens downward by default, but a downward peek
 * from a row near the bottom would overflow its scroll container — growing
 * the scrollable area and snapping the scrollbar — so when the rendered
 * card crosses the container's (or viewport's) bottom edge it flips above
 * the row instead. Measured once per open in a layout effect (the peek is
 * pointer-events: none, so flipping can't re-trigger hover).
 */
function usePeekFlip() {
  const ref = useRef<HTMLDivElement>(null);
  const [up, setUp] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let boundary = window.innerHeight;
    for (let a = el.parentElement; a; a = a.parentElement) {
      const oy = getComputedStyle(a).overflowY;
      if (oy === "auto" || oy === "scroll") {
        boundary = Math.min(boundary, a.getBoundingClientRect().bottom);
        break;
      }
    }
    if (el.getBoundingClientRect().bottom > boundary) setUp(true);
  }, []);
  return { ref, up };
}

/** Capped input/result preview under the hovered row (mockup's .peek). */
function Peek({
  entry,
  status,
  clickable,
}: {
  entry: ActivityEntry;
  status?: ToolStatus;
  clickable: boolean;
}) {
  const { ref, up } = usePeekFlip();
  const cls = up ? `${styles.peek} ${styles.peekUp}` : styles.peek;
  if (entry.type === "thinking") {
    return (
      <div className={cls} ref={ref} data-testid="activity-peek" data-up={up || undefined}>
        <div className={styles.lbl}>thinking</div>
        <pre>{entry.text.slice(0, PEEK_TEXT_CAP)}</pre>
      </div>
    );
  }
  const tool = entry.tool;
  const hunks = diffHunks(tool);
  const input = hunks ? undefined : inputPreview(tool);
  const result = tool.result;
  return (
    <div className={cls} ref={ref} data-testid="activity-peek" data-up={up || undefined}>
      <div className={styles.lbl}>{tool.name} · input</div>
      {hunks ? (
        <DiffView hunks={hunks} lineCap={PEEK_DIFF_LINES} />
      ) : (
        <pre>{input ?? "no input"}</pre>
      )}
      {result?.text && (
        <>
          <div className={styles.lbl}>{resultLabel(result, status)}</div>
          <pre>
            {result.text.length > PEEK_TEXT_CAP
              ? `${result.text.slice(0, PEEK_TEXT_CAP)}…`
              : result.text}
          </pre>
        </>
      )}
      {clickable && <div className={styles.peekHint}>click for full detail</div>}
    </div>
  );
}
