import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { Delegate, Envelope } from "../../../router/types";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { SubagentStream } from "../../../conversation/activity/ActivityDock";
import { foldHistoryEvents, mergeDelegates } from "../../../conversation/activity/conversationItems";
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
 * The stream comes from the mads event log (session.subagent_event), the
 * only source today — subject to GET /events' cap on very long streams.
 */
export function DelegateView({ sessionId, harnessId, parentName, routerClient, onBack }: DelegateViewProps) {
  const [record, setRecord] = useState<Delegate>();
  const [missing, setMissing] = useState(false);
  const [envelopes, setEnvelopes] = useState<Envelope[]>([]);

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
      unsubRecord();
      unsubStream();
    };
  }, [routerClient, sessionId, harnessId]);

  const history = useMemo(() => foldHistoryEvents(envelopes), [envelopes]);
  const model = useMemo(() => {
    const merged = record ? mergeDelegates(history.tasks, new Map([[harnessId, record]])) : history.tasks;
    return merged.get(harnessId);
  }, [history.tasks, record, harnessId]);
  const anchor = record?.tool_use_id ?? model?.toolUseId;
  const items = (anchor && history.subagentStreams.get(anchor)) || [];
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
        {record && <div className={styles.facts}>{facts(record)}</div>}
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
