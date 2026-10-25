import { useEffect, useState } from "react";
import { subscribeToEvents } from "../router/events";
import type { RouterClient } from "../router/client";
import { StatusMark } from "../features/sessions/rail/StatusMark";
import type { SessionProvisioningPayload } from "../router/types";
import { reduceSteps, type StepId } from "./provisioningSteps";
import styles from "./ProvisioningStrip.module.css";

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
  /** Read once after subscribing, to catch a start that finished before the strip mounted. */
  routerClient?: Pick<RouterClient, "getSession">;
  onDone?: () => void;
}

/** Ephemeral setup indicator — unmounts once the session goes active, not a persistent status bar. */
export function ProvisioningStrip({ sessionId, routerClient, onDone }: ProvisioningStripProps) {
  const [received, setReceived] = useState<SessionProvisioningPayload[]>([]);
  const [sessionActive, setSessionActive] = useState(false);

  useEffect(() => {
    setReceived([]);
    setSessionActive(false);
    const unsubscribe = subscribeToEvents(
      { sessionId, types: ["session.provisioning", "session.activity_changed"] },
      (envelope) => {
        if (envelope.type === "session.provisioning") {
          const payload = envelope.payload as SessionProvisioningPayload | undefined;
          if (payload?.step) {
            setReceived((prev) => [...prev, payload]);
          }
          // The runner's last step, sent right before the session goes
          // active. It also counts as done because the window shares one
          // event stream: an activity_changed delivered to the rail before
          // this strip subscribed is never delivered again, while the
          // provisioning history is replayed to the strip on subscribe.
          if (payload?.step === "provider_ready") setSessionActive(true);
        } else if (envelope.type === "session.activity_changed") {
          setSessionActive(true);
        }
      },
    );
    // A start that finished before the strip subscribed, with no replay to
    // carry its steps (session.provisioning was already on the stream for an
    // earlier session): only the record says it is over. Read after
    // subscribing, so a start finishing in between still arrives live.
    let cancelled = false;
    routerClient
      ?.getSession(sessionId)
      .then((session) => {
        if (!cancelled && session.activity === "active") setSessionActive(true);
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
  const latest = new Map(received.map((p) => [p.step, p]));
  const steps = reduceSteps(
    received.map((p) => p.step),
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
