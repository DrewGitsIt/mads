import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { getAllEvents } from "../../../router/eventPages";
import type { Delegate, DelegateCapabilities, Envelope, ToolUse } from "../../../router/types";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { ActivityDock, SubagentStream } from "../../../conversation/activity/ActivityDock";
import { WorkingIndicator } from "../../../conversation/ChatRenderer/WorkingIndicator";
import {
  foldHistoryEvents,
  mergeDelegates,
  transcriptSubStream,
} from "../../../conversation/activity/conversationItems";
import { KindIcon } from "../../../conversation/activity/primitives";
import { useDelegateFile } from "../../../conversation/activity/useDelegateFile";
import { TaskCounters, TaskStatusGlyph } from "../../../conversation/activity/TaskCard";
import styles from "./DelegateView.module.css";

/**
 * What a delegate's view follows live on the event log: its own stream, the
 * lifecycle lines its live counters come from, and out-of-band tool status
 * changes. All on the shared control-plane connection — no per-view socket.
 */
export const DELEGATE_EVENT_TYPES = [
  "session.subagent_event",
  "session.subagent_task",
  "session.tool_status_changed",
];

/**
 * What it reads back from the log on open — all of it, in pages. Not the
 * stream itself: that comes from the delegate's own file, and only falls back
 * to the log (every subagent's, the bulk of a long session's log) when there
 * is no file.
 */
const DELEGATE_REPLAY_TYPES = ["session.subagent_task", "session.tool_status_changed"];

export interface DelegateViewProps {
  sessionId: string;
  harnessId: string;
  /** The parent session's name, for the breadcrumb. */
  parentName?: string;
  routerClient: RouterClient;
  /** Back to the parent session's conversation. */
  onBack: () => void;
}

/**
 * A delegate — a harness-run subagent (docs/DELEGATES-DESIGN.md) — as a
 * first-class, full-pane conversation: its record's state and live counters
 * up top, its whole stream below. Read-only: steering a delegate depends on
 * what its harness supports (the design's C2 capabilities), which this view
 * doesn't assume.
 *
 * The stream comes from the delegate's own transcript, where its harness
 * keeps one (GET .../delegates/{harnessId}/transcript): whole, with its
 * prompt, its own text and the harness's timestamps. Where it doesn't — or
 * not yet — it comes from what mads recorded live (session.subagent_event),
 * which has the tool calls but none of the delegate's text, read back in
 * full only then.
 */
export function DelegateView({ sessionId, harnessId, parentName, routerClient, onBack }: DelegateViewProps) {
  const [record, setRecord] = useState<Delegate>();
  const [missing, setMissing] = useState(false);
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);
  // Live and replayed envelopes meet here, deduped by seq, in log order.
  const addEnvelopes = useCallback((incoming: Envelope[]) => {
    setEnvelopes((cur) => {
      const seen = new Set(cur.map((e) => e.seq));
      const fresh = incoming.filter((e) => !seen.has(e.seq));
      return fresh.length === 0 ? cur : [...cur, ...fresh].sort((a, b) => a.seq - b.seq);
    });
  }, []);

  useEffect(() => {
    let cancelled = false;
    setRecord(undefined);
    setMissing(false);
    setEnvelopes([]);
    // Subscribed before the reads, so nothing falls in the gap; the reads
    // merge under anything live (dedupe by seq; newest record wins).
    const unsubRecord = subscribeToEvents({ sessionId, types: ["session.delegate_changed"] }, (env) => {
      const d = env.payload as Delegate | undefined;
      if (d?.harness_id === harnessId) setRecord(d);
    });
    const unsubStream = subscribeToEvents({ sessionId, types: DELEGATE_EVENT_TYPES }, (env) => addEnvelopes([env]));
    routerClient
      .getSessionDelegates(sessionId)
      .then((list) => {
        if (cancelled) return;
        const d = list.find((x) => x.harness_id === harnessId);
        if (d) setRecord((cur) => cur ?? d);
        else setMissing(true);
      })
      .catch(() => {
        if (!cancelled) setMissing(true);
      });
    getAllEvents(routerClient, { sessionId, types: DELEGATE_REPLAY_TYPES })
      .then((history) => {
        if (!cancelled) addEnvelopes(history);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      unsubRecord();
      unsubStream();
    };
  }, [routerClient, sessionId, harnessId, addEnvelopes]);

  // Its own file, re-read once activity settles: new events while it runs,
  // and the record's last change when it ends (its final lines land then).
  const lastChange = `${envelopes.length}/${record?.state ?? ""}/${record?.updated_at ?? ""}`;
  const file = useDelegateFile(routerClient, sessionId, harnessId, lastChange);

  // No file: the stream falls back to what mads recorded live, read back in
  // full only now.
  const noFile = file?.available === false;
  useEffect(() => {
    if (!noFile) return;
    let cancelled = false;
    getAllEvents(routerClient, { sessionId, types: ["session.subagent_event"] })
      .then((stream) => {
        if (!cancelled) addEnvelopes(stream);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [noFile, routerClient, sessionId, addEnvelopes]);

  const history = useMemo(() => foldHistoryEvents(envelopes), [envelopes]);
  const model = useMemo(() => {
    const merged = record ? mergeDelegates(history.tasks, new Map([[harnessId, record]])) : history.tasks;
    return merged.get(harnessId);
  }, [history.tasks, record, harnessId]);
  const anchor = record?.tool_use_id ?? model?.toolUseId;
  const fromFile = useMemo(() => (file?.available ? transcriptSubStream(file.messages) : undefined), [file]);
  const items = useMemo(
    () => fromFile ?? ((anchor && history.subagentStreams.get(anchor)) || []),
    [fromFile, anchor, history.subagentStreams],
  );
  const title = record?.description || model?.description || record?.type || "Subagent";
  const type = record?.type ?? model?.subagentType;
  const running = model?.running ?? false;

  // One call's full detail in the bottom dock, as in the main conversation.
  // Held by id and resolved from the stream each render, so an open call
  // picks up its result when the next read brings it.
  const [openTool, setOpenTool] = useState<{ id?: string; tool: ToolUse }>();
  const closeTool = useCallback(() => setOpenTool(undefined), []);
  const dockEntry = useMemo(() => {
    if (!openTool) return undefined;
    const found = openTool.id
      ? items.find((i) => i.type === "tool" && i.tool.id === openTool.id)
      : undefined;
    return found?.type === "tool" ? found : { type: "tool" as const, tool: openTool.tool, timestamp: undefined };
  }, [openTool, items]);

  // The working indicator's ticker: the newest call still in flight.
  const inFlight = useMemo(() => {
    if (!running) return undefined;
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.type === "tool" && !it.tool.result) return it.tool;
    }
    return undefined;
  }, [items, running]);

  // Scroll: open at the tail and follow it while the subagent runs, but let
  // go the moment the user scrolls up — the conversation pane's rule
  // (ConversationTab's stickToBottom).
  const bodyRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const handleScroll = useCallback(() => {
    const el = bodyRef.current;
    if (!el) return;
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  }, []);
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight;
  }, [items, running, missing]);

  return (
    <section className={styles.view} aria-label={`Subagent: ${title}`} data-testid="delegate-view">
      <header className={styles.head}>
        <button type="button" className={styles.back} onClick={onBack}>
          ← {parentName || "Parent session"}
        </button>
        <div className={styles.titleRow}>
          <KindIcon kind="agent" />
          <h2 className={styles.title}>{title}</h2>
          {type && <span className={styles.type}>{type}</span>}
          <span className={styles.meta}>
            {model && (
              <span data-testid="delegate-counters">
                <TaskCounters model={model} />
              </span>
            )}
            <TaskStatusGlyph model={model} />
          </span>
        </div>
        {record && (
          <div className={styles.facts}>
            {facts(record)}
            {file && (fromFile ? " · From its own transcript" : " · As mads recorded it live")}
          </div>
        )}
        {record?.capabilities && <Capabilities caps={record.capabilities} worktree={record.worktree_path} />}
      </header>
      <div className={styles.body} ref={bodyRef} onScroll={handleScroll} data-testid="delegate-stream">
        <ContentColumn>
          {missing && !record ? (
            <div className={styles.note}>This subagent's record isn't available any more.</div>
          ) : (
            <SubagentStream
              items={items}
              running={running}
              summary={model?.summary}
              statuses={history.statuses}
              selectedToolId={openTool?.id}
              onOpenTool={(tool) => setOpenTool({ id: tool.id, tool })}
            />
          )}
        </ContentColumn>
        {running && (
          <div className={styles.pinned}>
            <WorkingIndicator tool={inFlight} since={model?.startedAt ?? record?.started_at} />
          </div>
        )}
      </div>
      {dockEntry && (
        <ActivityDock
          view={{ type: "tool", tool: dockEntry.tool, timestamp: dockEntry.timestamp }}
          sessionId={sessionId}
          client={routerClient}
          statuses={history.statuses}
          onClose={closeTool}
        />
      )}
    </section>
  );
}

/** One line of what the record knows beyond the header: how it ran and how it ended. */
function facts(d: Delegate): string {
  const parts: string[] = [];
  parts.push(d.backgrounded ? "Ran in the background" : "Ran in the foreground");
  if (d.runs > 1) parts.push(`${d.runs} runs`);
  if (d.state !== "running") {
    const why = d.reason && d.reason !== d.state ? ` (${d.reason.replace("_", " ")})` : "";
    parts.push(`${d.state[0].toUpperCase()}${d.state.slice(1)}${why}${d.inferred ? " — concluded by mads, no report from the agent" : ""}`);
  }
  return parts.join(" · ");
}

/**
 * What can be done with this delegate, as resolved for it (matrix defaults
 * corrected at run time). Stated as facts, not offered as controls: the
 * actions they enable — relaying a message, stopping it alone, continuing it
 * as a session — aren't built yet, and a control that does nothing would be
 * worse than a sentence.
 */
function Capabilities({ caps, worktree }: { caps: DelegateCapabilities; worktree?: string }) {
  const items: { label: string; title: string }[] = [
    caps.transcript
      ? { label: "Own transcript on disk", title: "The harness kept this subagent's conversation in a file of its own." }
      : { label: "No transcript of its own", title: "Its history is only what mads recorded from the live stream." },
    caps.steer === "direct"
      ? { label: "Can be messaged directly", title: "A message can be sent to this subagent itself." }
      : caps.steer === "relay"
        ? { label: "Reachable through its parent", title: "A message can only reach it by asking the parent agent to pass it on — the parent decides how faithfully." }
        : { label: "Watch only", title: "Nothing can send this subagent a message." },
    caps.stop
      ? { label: "Can be stopped on its own", title: "It can be stopped without stopping its parent." }
      : caps.stops_with_parent
        ? { label: "Stops with its parent", title: "Stopping the parent session's turn stops this subagent too." }
        : { label: "Can't be stopped from mads", title: "Neither it nor its parent's stop reaches it." },
  ];
  if (caps.continuable) {
    items.push({ label: "Could continue as a session", title: "Its transcript can seed a new session of your own (a fork) — not yet available in mads." });
  }
  if (worktree) items.push({ label: `Worktree ${worktree}`, title: "It ran in a separate worktree." });
  return (
    <ul className={styles.caps} aria-label="What this subagent supports" data-testid="delegate-capabilities">
      {items.map((i) => (
        <li key={i.label} title={i.title}>
          {i.label}
        </li>
      ))}
    </ul>
  );
}
