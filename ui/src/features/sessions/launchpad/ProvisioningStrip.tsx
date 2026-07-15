import { useEffect, useState } from "react";
import { subscribeToEvents } from "../../../router/events";
import { reduceSteps, type StepId } from "./provisioningSteps";
import styles from "./ProvisioningStrip.module.css";

const STEP_LABELS: Record<StepId, string> = {
  worktree: "Setting up worktree",
  hook: "Installing commit hook",
  agent_starting: "Starting agent",
  mcp_up: "Connecting capabilities",
  provider_ready: "Ready",
};

export interface ProvisioningStripProps {
  sessionId: string;
  onDone?: () => void;
}

/** Ephemeral setup indicator — unmounts once the session goes active, not a persistent status bar. */
export function ProvisioningStrip({ sessionId, onDone }: ProvisioningStripProps) {
  const [receivedSteps, setReceivedSteps] = useState<string[]>([]);
  const [sessionActive, setSessionActive] = useState(false);

  useEffect(() => {
    setReceivedSteps([]);
    setSessionActive(false);
    const unsubscribe = subscribeToEvents(
      { sessionId, types: ["session.provisioning", "session.activity_changed"] },
      (envelope) => {
        if (envelope.type === "session.provisioning") {
          const payload = envelope.payload as { step?: string } | undefined;
          if (payload?.step) {
            setReceivedSteps((prev) => [...prev, payload.step as string]);
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

  const steps = reduceSteps(receivedSteps, sessionActive);

  return (
    <div className={styles.strip} role="status" aria-label="Session provisioning">
      {steps.map((step) => (
        <span
          key={step.id}
          className={[styles.step, styles[step.status]].join(" ")}
        >
          {STEP_LABELS[step.id]}
        </span>
      ))}
    </div>
  );
}
