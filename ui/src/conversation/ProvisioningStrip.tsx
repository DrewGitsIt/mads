import { useEffect, useState } from "react";
import { subscribeToEvents } from "../router/events";
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
  onDone?: () => void;
}

/** Ephemeral setup indicator — unmounts once the session goes active, not a persistent status bar. */
export function ProvisioningStrip({ sessionId, onDone }: ProvisioningStripProps) {
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
        } else if (envelope.type === "session.activity_changed") {
          setSessionActive(true);
        }
      },
    );
    return unsubscribe;
  }, [sessionId]);

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
          {step.id === "worktree" && latest.get("worktree")?.mode === "in_place"
            ? IN_PLACE_LABEL
            : STEP_LABELS[step.id]}
        </span>
      ))}
    </div>
  );
}
