import { useEffect, useMemo, useRef, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { Delegate, DelegateCapabilities, DelegateTranscript, Envelope } from "../../../router/types";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { SubagentStream } from "../../../conversation/activity/ActivityDock";
import {
  foldHistoryEvents,
  mergeDelegates,
  transcriptSubStream,
} from "../../../conversation/activity/conversationItems";
import { KindIcon } from "../../../conversation/activity/primitives";
import { TaskCounters, TaskStatusGlyph } from "../../../conversation/activity/TaskCard";
import styles from "./DelegateView.module.css";

/**
 * What a delegate's view reads from the event log: its own stream, the
 * lifecycle lines its live counters come from, and out-of-band tool status
 * changes. All on the shared control-plane connection — no per-view socket.
 */
export const DELEGATE_EVENT_TYPES = [
  "session.subagent_event",
  "session.subagent_task",
  "session.tool_status_changed",
];

/**
 * How long the view waits after the last sign of activity before re-reading
 * the delegate's transcript file. The harness writes the file as the delegate
 * runs, but nothing announces a write: a live event for the delegate (or a
 * change to its record) is the cue, and this batches a burst of them into one
 * read.
 */
export const DELEGATE_FILE_REFRESH_MS = 1000;

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
 * which has the tool calls but none of the delegate's text, and is subject
 * to GET /events' cap on very long streams.
 */
export function DelegateView({ sessionId, harnessId, parentName, routerClient, onBack }: DelegateViewProps) {
  const [record, setRecord] = useState<Delegate>();
  const [missing, setMissing] = useState(false);
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);
  const [file, setFile] = useState<DelegateTranscript>();
  // Only the newest read may land: a slow early read must not replace a
  // later, longer one.
  const fileRead = useRef(0);
  const readFile = useMemo(
    () => () => {
      const n = ++fileRead.current;
      routerClient
        .getDelegateTranscript(sessionId, harnessId)
        .then((t) => {
          if (n === fileRead.current) setFile(t);
        })
        .catch(() => {});
    },
    [routerClient, sessionId, harnessId],
  );

  useEffect(() => {
    let cancelled = false;
    setRecord(undefined);
    setMissing(false);
    setEnvelopes([]);
    setFile(undefined);
    readFile();
    // Subscribed before the reads, so nothing falls in the gap; the reads
    // merge under anything live (dedupe by seq; newest record wins).
    const unsubRecord = subscribeToEvents({ sessionId, types: ["session.delegate_changed"] }, (env) => {
      const d = env.payload as Delegate | undefined;
      if (d?.harness_id === harnessId) setRecord(d);
    });
    const unsubStream = subscribeToEvents({ sessionId, types: DELEGATE_EVENT_TYPES }, (env) => {
      setEnvelopes((cur) => (cur.some((e) => e.seq === env.seq) ? cur : [...cur, env]));
    });
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
    routerClient
      .getEvents({ sessionId, types: DELEGATE_EVENT_TYPES })
      .then((history) => {
        if (cancelled) return;
        setEnvelopes((live) => {
          const seen = new Set(live.map((e) => e.seq));
          return [...history.filter((e) => !seen.has(e.seq)), ...live];
        });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      fileRead.current++;
      unsubRecord();
      unsubStream();
    };
  }, [routerClient, sessionId, harnessId, readFile]);

  // Re-read the file once activity settles: new events while it runs, and
  // the record's last change when it ends (its final lines land then).
  const lastChange = `${envelopes.length}/${record?.state ?? ""}/${record?.updated_at ?? ""}`;
  const firstChange = useRef(lastChange);
  useEffect(() => {
    if (lastChange === firstChange.current) return;
    const t = setTimeout(readFile, DELEGATE_FILE_REFRESH_MS);
    return () => clearTimeout(t);
  }, [lastChange, readFile]);

  const history = useMemo(() => foldHistoryEvents(envelopes), [envelopes]);
  const model = useMemo(() => {
    const merged = record ? mergeDelegates(history.tasks, new Map([[harnessId, record]])) : history.tasks;
    return merged.get(harnessId);
  }, [history.tasks, record, harnessId]);
  const anchor = record?.tool_use_id ?? model?.toolUseId;
  const fromFile = useMemo(() => (file?.available ? transcriptSubStream(file.messages) : undefined), [file]);
  const items = fromFile ?? ((anchor && history.subagentStreams.get(anchor)) || []);
  const title = record?.description || model?.description || record?.type || "Subagent";
  const type = record?.type ?? model?.subagentType;

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
      <div className={styles.body}>
        <ContentColumn>
          {missing && !record ? (
            <div className={styles.note}>This subagent's record isn't available any more.</div>
          ) : (
            <SubagentStream
              items={items}
              running={model?.running ?? false}
              summary={model?.summary}
              statuses={history.statuses}
            />
          )}
        </ContentColumn>
      </div>
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
