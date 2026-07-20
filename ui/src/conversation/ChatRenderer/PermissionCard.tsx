import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { PermissionRequest } from "../../router/types";
import styles from "./PermissionCard.module.css";

export interface PermissionCardProps {
  request: PermissionRequest;
  onResolve: (id: string, decision: "allow" | "deny") => Promise<void>;
}

const STATUS_LABEL: Record<PermissionRequest["status"], string> = {
  pending: "awaiting your decision",
  allowed: "allowed",
  denied: "denied",
  expired: "expired — no response in time",
};

function summarizeInput(input: unknown): string | undefined {
  if (input === undefined || input === null) return undefined;
  if (typeof input === "object") {
    const entries = Object.entries(input as Record<string, unknown>);
    const first = entries[0];
    return first ? `${first[0]}: ${String(first[1]).slice(0, 80)}` : undefined;
  }
  return String(input);
}

/**
 * Inline Allow/Deny card for a chat-transport permission prompt
 * (TrustLimited/TrustReadOnly — see providers/claudecode/README.md "Trust
 * enforcement"). Renders from the live `permission_request` stream event or
 * from GET /permission-requests on mount/refetch, either way keyed by
 * `request.id` — this component itself never distinguishes which source it
 * came from.
 */
export function PermissionCard({ request, onResolve }: PermissionCardProps) {
  const [resolving, setResolving] = useState<"allow" | "deny">();
  const pending = request.status === "pending";
  const summary = summarizeInput(request.input);

  const handle = async (decision: "allow" | "deny") => {
    setResolving(decision);
    try {
      await onResolve(request.id, decision);
    } finally {
      setResolving(undefined);
    }
  };

  return (
    <Card className={styles.card} data-status={request.status}>
      <div className={styles.header}>
        <Chip tone="warn">permission</Chip>
        <span className={styles.tool}>{request.tool}</span>
        <Chip tone={pending ? "warn" : request.status === "allowed" ? "ok" : "bad"}>
          {STATUS_LABEL[request.status]}
        </Chip>
      </div>
      {summary && <div className={styles.summary}>{summary}</div>}
      {pending && (
        <div className={styles.actions}>
          <Button
            variant="primary"
            size="sm"
            disabled={resolving !== undefined}
            onClick={() => void handle("allow")}
          >
            {resolving === "allow" ? "Allowing…" : "Allow"}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={resolving !== undefined}
            onClick={() => void handle("deny")}
          >
            {resolving === "deny" ? "Denying…" : "Deny"}
          </Button>
        </div>
      )}
    </Card>
  );
}
