import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import { FOLD_MS } from "../../components/Motion";
import type { PermissionRequest } from "../../router/types";
import styles from "./PermissionCard.module.css";

export interface PermissionCardProps {
  request: PermissionRequest;
  onResolve: (id: string, decision: "allow" | "deny") => Promise<void>;
  /**
   * The card just arrived while the user was watching: once it has folded
   * in, a single soft ring says "this needs you". Read at mount; only a
   * pending card rings.
   */
  ring?: boolean;
}

// The ring waits out the fold (the fold clips until it settles), then runs once.
const RING_MS = 900;

const STATUS_LABEL: Record<PermissionRequest["status"], string> = {
  pending: "awaiting your decision",
  allowed: "allowed",
  denied: "denied",
  expired: "expired — no response in time",
  cancelled: "cancelled — the agent stopped waiting",
};

const STATUS_TONE: Record<PermissionRequest["status"], ChipTone> = {
  pending: "warn",
  allowed: "ok",
  denied: "bad",
  expired: "bad",
  cancelled: "neutral",
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
 * came from. Where it renders is the caller's choice: under its tool call's
 * row when `tool_use_id` anchors it, else in the turn's trailing list.
 *
 * The wrapper's data-permission-id/-status let the conversation's approval
 * jump find every copy of a card (main stream, dock) without threading refs,
 * and count as "inside" for the dock's outside-click close.
 */
export function PermissionCard({ request, onResolve, ring = false }: PermissionCardProps) {
  const [resolving, setResolving] = useState<"allow" | "deny">();
  const pending = request.status === "pending";
  // Dropped once played, so a later approval-jump flash ending can't
  // hand the animation back to the ring and replay it.
  const [ringing, setRinging] = useState(ring && pending);
  useEffect(() => {
    if (!ringing) return;
    const t = window.setTimeout(() => setRinging(false), FOLD_MS + RING_MS);
    return () => window.clearTimeout(t);
  }, [ringing]);
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
    <div
      className={styles.anchor}
      data-permission-id={request.id}
      data-permission-status={request.status}
      data-ring={ringing || undefined}
    >
      {/* A status class, not data-status: Card doesn't forward data-* attributes. */}
      <Card className={`${styles.card} ${styles[request.status] ?? ""}`}>
        <div className={styles.header}>
          <Chip tone="warn">permission</Chip>
          <span className={styles.tool}>{request.tool}</span>
          <Chip tone={STATUS_TONE[request.status]}>{STATUS_LABEL[request.status]}</Chip>
        </div>
        {request.reason && <div className={styles.reason}>{request.reason}</div>}
        {summary && <div className={styles.summary}>{summary}</div>}
        {pending && (
          <div className={styles.actions}>
            <Button
              variant="primary"
              size="sm"
              data-permission-action="allow"
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
    </div>
  );
}
