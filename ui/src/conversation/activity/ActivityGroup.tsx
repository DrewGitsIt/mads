import { useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Chip } from "../../components/Chip/Chip";
import { Fold, useArrivals } from "../../components/Motion";
import type { BackgroundTask, PermissionRequest, ToolKind, ToolStatus, ToolUse } from "../../router/types";
import type { PermissionReveal } from "../ApprovalJump/permissionReveal";
import { PermissionCard } from "../ChatRenderer/PermissionCard";
import { BackgroundElapsed } from "./Background";
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
   * This group belongs to the in-flight turn: it folds in when it first
   * appears, renders open, and its new rows and permission cards fold in.
   * When the turn ends (live flips false) the group lingers open for
   * LINGER_MS, then folds itself closed — the replay/transcript re-render
   * never animates (same contract ToolCard's `live` prop kept).
   */
  live?: boolean;
  /** Out-of-band status overrides (session.tool_status_changed), by tool_use id. */
  statuses?: ReadonlyMap<string, ToolStatus>;
  /** Calls still running as background processes, by tool_use id: they spin with a running clock. */
  background?: ReadonlyMap<string, BackgroundTask>;
  /** Row whose detail is open in the dock — suppresses that row's hover-peek. */
  selectedToolId?: string;
  /** Row click → open/retarget the dock. Absent = rows are inert (subagent dock view). */
  onOpenTool?: (tool: ToolUse) => void;
  /** Open on mount regardless of liveness (dock's subagent stream view). */
  defaultOpen?: boolean;
  /** Permission requests anchored to a call, by tool_use id — each renders right under its row. */
  permissions?: ReadonlyMap<string, PermissionRequest[]>;
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
  /** The approval jump's latest reveal — opens this group if it holds that pending card. */
  reveal?: PermissionReveal | null;
}

const PEEK_TEXT_CAP = 400;
/** How long a finished live group stays open, so the last ✓ is seen landing, before it folds away. */
export const LINGER_MS = 600;
const PEEK_DIFF_LINES = 8;
const noResolve = async () => {};

function entryStatus(
  entry: ActivityEntry,
  statuses?: ReadonlyMap<string, ToolStatus>,
  live?: boolean,
  background?: ReadonlyMap<string, BackgroundTask>,
): ToolStatus | undefined {
  if (entry.type !== "tool") return undefined;
  // Its result came back at launch; the process it launched hasn't ended.
  if (entry.tool.id && background?.has(entry.tool.id)) return "in_progress";
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
function aggregateStatus(
  entries: ActivityEntry[],
  statuses?: ReadonlyMap<string, ToolStatus>,
  live?: boolean,
  background?: ReadonlyMap<string, BackgroundTask>,
): ToolStatus {
  const all = entries
    .map((e) => entryStatus(e, statuses, live, background))
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
  background,
  selectedToolId,
  onOpenTool,
  defaultOpen = false,
  permissions,
  onResolvePermission,
  reveal,
}: ActivityGroupProps) {
  const [open, setOpen] = useState(live || defaultOpen);
  // A group that mounts mid-turn has just arrived; one from history hasn't.
  const [arrivedLive] = useState(live);
  // Collapse exactly once when the live turn ends, after a short linger —
  // never re-collapse a group the user has since reopened (or touched
  // during the linger: their click clears it).
  const wasLive = useRef(live);
  const linger = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (wasLive.current && !live) {
      linger.current = window.setTimeout(() => setOpen(false), LINGER_MS);
    }
    if (live) {
      window.clearTimeout(linger.current);
      setOpen(true);
    }
    wasLive.current = live;
  }, [live]);
  useEffect(() => () => window.clearTimeout(linger.current), []);
  const toggle = () => {
    window.clearTimeout(linger.current);
    setOpen((o) => !o);
  };

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
  // The approval jump re-reveals one of ours: open during render (not in an
  // effect) so the card is in the DOM by the commit the jump scrolls after.
  const [seenReveal, setSeenReveal] = useState(reveal?.nonce);
  if (reveal && reveal.nonce !== seenReveal) {
    setSeenReveal(reveal.nonce);
    if (awaiting.split(",").includes(reveal.id)) setOpen(true);
  }

  // Rows and permission cards that join after mount fold in; what was
  // already here (history, or a group remounted) never does.
  const keys = entries.map(entryKey);
  const permissionKeys = entries.flatMap((e) =>
    e.type === "tool" && e.tool.id ? (permissions?.get(e.tool.id) ?? []).map((r) => `p:${r.id}`) : [],
  );
  const arrived = useArrivals([...keys, ...permissionKeys], live);

  const { kinds, label } = kindCounts(entries);
  const duration = useMemo(() => groupDuration(entries), [entries]);
  const agg = aggregateStatus(entries, statuses, live, background);
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
    <Fold appear={arrivedLive} fade>
      <div className={styles.group} data-open={open || undefined} data-testid="activity-group">
        <button
          type="button"
          className={styles.head}
          aria-expanded={open}
          onClick={toggle}
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
        <Fold open={open}>
          <div className={styles.body}>
            {entries.map((entry, i) => {
              const requests =
                entry.type === "tool" && entry.tool.id ? permissions?.get(entry.tool.id) : undefined;
              return (
                <Fold key={keys[i]} appear={arrived(keys[i])} fade className={styles.rowFold}>
                  <ActivityRow
                    entry={entry}
                    status={entryStatus(entry, statuses, live, background)}
                    background={
                      entry.type === "tool" && entry.tool.id ? background?.get(entry.tool.id) : undefined
                    }
                    selected={
                      entry.type === "tool" &&
                      entry.tool.id !== undefined &&
                      entry.tool.id === selectedToolId
                    }
                    onOpen={onOpenTool}
                  />
                  {/* Under its row, in the row's own fold: the card belongs to that call. */}
                  {requests?.map((req) => {
                    const fresh = arrived(`p:${req.id}`);
                    return (
                      <Fold key={req.id} appear={fresh} fade>
                        <div className={styles.permission}>
                          <PermissionCard
                            request={req}
                            onResolve={onResolvePermission ?? noResolve}
                            ring={fresh}
                          />
                        </div>
                      </Fold>
                    );
                  })}
                </Fold>
              );
            })}
          </div>
        </Fold>
      </div>
    </Fold>
  );
}

function entryKey(entry: ActivityEntry, i: number): string {
  return entry.type === "tool" ? (entry.tool.id ?? `t${i}`) : `th${i}`;
}

function firstLine(text: string, cap = 100): string {
  const line = text.split("\n", 1)[0];
  return line.length > cap ? `${line.slice(0, cap)}…` : line;
}

export function ActivityRow({
  entry,
  status,
  background,
  selected,
  onOpen,
}: {
  entry: ActivityEntry;
  status?: ToolStatus;
  /** Still running as a background process: a running clock in place of the call's duration. */
  background?: BackgroundTask;
  selected?: boolean;
  onOpen?: (tool: ToolUse) => void;
}) {
  const [hovered, setHovered] = useState(false);
  const rowRef = useRef<HTMLDivElement>(null);
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
        {background ? (
          <BackgroundElapsed since={background.started_at} />
        ) : (
          duration && <span>{duration}</span>
        )}
        {status &&
          (status === "in_progress" ? <Spinner /> : <StatusGlyph status={status} />)}
      </span>
    </>
  );

  const rowCls = [styles.row, entry.type === "thinking" ? styles.think : ""].filter(Boolean).join(" ");

  return (
    <div
      ref={rowRef}
      className={rowCls}
      data-sel={selected || undefined}
      data-background-id={background ? tool?.id : undefined}
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
      {peekVisible && (
        <Peek entry={entry} status={status} clickable={clickable} anchor={rowRef} />
      )}
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

// Peek geometry, mirroring the old in-row CSS: inset past the kind icon,
// min(540px, 90% of the row) wide, a 2px gap off the row.
const PEEK_INSET = 40;
const PEEK_MAX_WIDTH = 540;
const PEEK_GAP = 2;
const PEEK_MARGIN = 8;

/**
 * Where a peek of `height` goes relative to its row, in viewport
 * coordinates: below when it fits, above when only that fits, otherwise the
 * roomier side clamped into the viewport. The viewport is the only boundary
 * — the peek is portaled out of every scroll container, so none can clip it
 * (the subagent dock's short body used to cut off every flipped-up peek).
 */
export function placePeek(
  row: Pick<DOMRect, "top" | "bottom">,
  height: number,
  viewportHeight: number,
): { top: number; up: boolean } {
  const below = row.bottom + PEEK_GAP;
  const above = row.top - PEEK_GAP - height;
  const maxTop = viewportHeight - PEEK_MARGIN - height;
  if (below <= maxTop) return { top: below, up: false };
  if (above >= PEEK_MARGIN) return { top: above, up: true };
  const up = row.top > viewportHeight - row.bottom;
  return { top: Math.max(PEEK_MARGIN, Math.min(up ? above : below, maxTop)), up };
}

/**
 * Positions the portaled peek against its row: width first (height depends
 * on it), then the vertical side. Runs before paint, and again on any
 * scroll (capture catches the stream's and the dock's own scrollers) or
 * resize so the card tracks its row. Writes style directly — the peek is
 * pointer-events: none, so moving it can't re-trigger hover.
 */
function usePeekPlacement(anchor: RefObject<HTMLElement | null>) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const place = () => {
      const el = ref.current;
      const row = anchor.current;
      if (!el || !row) return;
      const r = row.getBoundingClientRect();
      el.style.left = `${r.left + PEEK_INSET}px`;
      el.style.width = `${Math.min(PEEK_MAX_WIDTH, r.width * 0.9)}px`;
      const { top, up } = placePeek(r, el.getBoundingClientRect().height, window.innerHeight);
      el.style.top = `${top}px`;
      if (up) el.dataset.up = "true";
      else delete el.dataset.up;
    };
    place();
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [anchor]);
  return ref;
}

/** Capped input/result preview beside the hovered row (mockup's .peek), portaled to the body. */
function Peek({
  entry,
  status,
  clickable,
  anchor,
}: {
  entry: ActivityEntry;
  status?: ToolStatus;
  clickable: boolean;
  anchor: RefObject<HTMLElement | null>;
}) {
  const ref = usePeekPlacement(anchor);
  return createPortal(
    <div className={styles.peek} ref={ref} data-testid="activity-peek">
      <PeekBody entry={entry} status={status} clickable={clickable} />
    </div>,
    document.body,
  );
}

function PeekBody({
  entry,
  status,
  clickable,
}: {
  entry: ActivityEntry;
  status?: ToolStatus;
  clickable: boolean;
}) {
  if (entry.type === "thinking") {
    return (
      <>
        <div className={styles.lbl}>thinking</div>
        <pre>{entry.text.slice(0, PEEK_TEXT_CAP)}</pre>
      </>
    );
  }
  const tool = entry.tool;
  const hunks = diffHunks(tool);
  const input = hunks ? undefined : inputPreview(tool);
  const result = tool.result;
  return (
    <>
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
    </>
  );
}
