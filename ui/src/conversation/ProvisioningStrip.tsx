import { useEffect, useState } from "react";
import { eventsReady, subscribeToEvents } from "../router/events";
import { getAllEvents } from "../router/eventPages";
import type { RouterClient } from "../router/client";
import { StatusMark } from "../features/sessions/rail/StatusMark";
import type { Envelope, SessionProvisioningPayload } from "../router/types";
import { reduceSteps, type StepId } from "./provisioningSteps";
import styles from "./ProvisioningStrip.module.css";

const PROVISIONING_TYPES = ["session.provisioning", "session.activity_changed"];

/** One step as the log recorded it; its seq orders history and live steps together. */
interface ReceivedStep {
  seq: number;
  payload: SessionProvisioningPayload;
}

function stepOf(env: Envelope): ReceivedStep | undefined {
  if (env.type !== "session.provisioning") return undefined;
  const payload = env.payload as SessionProvisioningPayload | undefined;
  return payload?.step ? { seq: env.seq, payload } : undefined;
}

/** Merges steps by seq: in log order, each once, whichever way it arrived. */
function mergeSteps(prev: ReceivedStep[], incoming: ReceivedStep[]): ReceivedStep[] {
  const seen = new Set(prev.map((r) => r.seq));
  const fresh = incoming.filter((r) => !seen.has(r.seq));
  return fresh.length === 0 ? prev : [...prev, ...fresh].sort((a, b) => a.seq - b.seq);
}

const STEP_LABELS: Record<StepId, string> = {
  worktree: "Setting up worktree",
  hook: "Installing commit hook",
  mcp_up: "Connecting capabilities",
  agent_starting: "Starting agent",
  provider_ready: "Ready",
};

// The worktree step's label once its payload says the session runs in
// place. A payload without mode (older rows) keeps the worktree wording.
const IN_PLACE_LABEL = "Using current directory";

export interface ProvisioningStripProps {
  sessionId: string;
  /**
   * Read once after subscribing: the session's provisioning history (the
   * event bus never replays it) and the record, to catch a start that
   * finished before the strip mounted.
   */
  routerClient?: Pick<RouterClient, "getSession" | "getEvents">;
  onDone?: () => void;
}

/** Ephemeral setup indicator — unmounts once the session goes active, not a persistent status bar. */
export function ProvisioningStrip({ sessionId, routerClient, onDone }: ProvisioningStripProps) {
  const [received, setReceived] = useState<ReceivedStep[]>([]);
  const [sessionActive, setSessionActive] = useState(false);

  useEffect(() => {
    setReceived([]);
    setSessionActive(false);
    const unsubscribe = subscribeToEvents({ sessionId, types: PROVISIONING_TYPES }, (envelope) => {
      if (envelope.type === "session.provisioning") {
        const step = stepOf(envelope);
        if (step) setReceived((prev) => mergeSteps(prev, [step]));
        // The runner's last step, sent right before the session goes
        // active. It counts as done too: the activity_changed that follows
        // it may have reached the window before this strip subscribed.
        if (step?.payload.step === "provider_ready") setSessionActive(true);
      } else if (envelope.type === "session.activity_changed") {
        setSessionActive(true);
      }
    });
    // The bus never replays, so what happened before the strip subscribed
    // comes over REST: subscribed first, both reads behind the bus's ready
    // barrier (router/events.ts eventsReady), so nothing falls between them
    // and the stream; steps merge by seq, so one that came both ways counts
    // once and history lands in front of what arrived live.
    let cancelled = false;
    const ready = eventsReady();
    if (routerClient) {
      void ready
        .then(() => (cancelled ? [] : getAllEvents(routerClient, { sessionId, types: PROVISIONING_TYPES })))
        .then((history) => {
          if (cancelled) return;
          const steps = history.map(stepOf).filter((r): r is ReceivedStep => r !== undefined);
          if (steps.length === 0) return;
          setReceived((prev) => mergeSteps(prev, steps));
          // The latest start is over if its last step is provider_ready, or
          // the session's activity changed after it (an activity change
          // before it belongs to an earlier life of the session).
          const last = steps[steps.length - 1];
          if (
            last.payload.step === "provider_ready" ||
            history.some((env) => env.type === "session.activity_changed" && env.seq > last.seq)
          ) {
            setSessionActive(true);
          }
        })
        .catch(() => {
          // Best effort: live steps still arrive.
        });
    }
    // A start that finished before the strip subscribed but that the history
    // doesn't show finishing (from before these events existed, or past the
    // read's page cap): only the record says it is over.
    void ready
      .then(() => (cancelled ? undefined : routerClient?.getSession(sessionId)))
      .then((session) => {
        if (!cancelled && session?.activity === "active") setSessionActive(true);
      })
      .catch(() => {
        // Best effort: the live events still hide the strip.
      });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [sessionId, routerClient]);

  useEffect(() => {
    if (sessionActive) onDone?.();
  }, [sessionActive, onDone]);

  if (sessionActive) return null;

  // Latest payload per step: a restarted Start reports its steps again.
  const latest = new Map(received.map(({ payload }) => [payload.step, payload]));
  const steps = reduceSteps(
    received.map(({ payload }) => payload.step),
    sessionActive,
  ).filter((step) => !(step.id === "hook" && latest.get("hook")?.skipped));

  return (
    <div className={styles.strip} role="status" aria-label="Session provisioning">
      {steps.map((step) => (
        <span
          key={step.id}
          className={[styles.step, styles[step.status]].join(" ")}
        >
          {step.status === "active" ? (
            <StatusMark kind="working" />
          ) : step.status === "done" ? (
            <StatusMark kind="done" />
          ) : (
            <PendingMark />
          )}
          {step.id === "worktree" && latest.get("worktree")?.mode === "in_place"
            ? IN_PLACE_LABEL
            : STEP_LABELS[step.id]}
        </span>
      ))}
    </div>
  );
}

/** The working spinner's unlit track alone: not started yet. */
function PendingMark() {
  return (
    <svg className={styles.pendingMark} viewBox="0 0 10 10" width="10" height="10" fill="none" aria-hidden="true">
      <circle cx="5" cy="5" r="3.6" stroke="currentColor" strokeWidth="1.6" />
    </svg>
  );
}
