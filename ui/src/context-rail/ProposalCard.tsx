import { useState } from "react";
import type { RouterClient } from "../router/client";
import type { Proposal } from "../router/types";
import { Draft } from "../components/Draft/Draft";
import { Chip } from "../components/Chip/Chip";
import { Button } from "../components/Button/Button";
import styles from "./ProposalCard.module.css";

const KIND_LABELS: Record<Proposal["type"], string> = {
  create_task: "New task",
  add_note: "Note",
  update_task: "Update",
};

function summarize(proposal: Proposal): string {
  switch (proposal.type) {
    case "create_task":
      return proposal.task?.description ?? "";
    case "add_note":
      return proposal.note?.content ?? "";
    case "update_task": {
      const u = proposal.update;
      if (!u) return "";
      const fields = [
        u.title && "title",
        u.description && "description",
        u.acceptance_criteria && "acceptance criteria",
        u.scope && "scope",
      ].filter(Boolean);
      return fields.length ? `Proposed changes to ${fields.join(", ")}.` : "";
    }
  }
}

export interface ProposalCardProps {
  proposal: Proposal;
  routerClient: RouterClient;
  /** Title of the proposal's target task — required for add_note/update_task, unused for create_task. */
  targetTaskTitle?: string;
  onApproved?: (proposal: Proposal) => void;
  onDismissed?: (proposalId: string) => void;
}

/**
 * A synthesis proposal rendered as an approve/dismiss card, or — once
 * already applied (auto-approve config, spec §4's "power-user preference")
 * — a brief static notice. Border-pulses on mount (CSS animation) to draw
 * attention to a freshly arrived proposal; parent list components key by
 * proposal.id so React only mounts a fresh node (and replays the pulse) for
 * an id it hasn't rendered before.
 */
export function ProposalCard({
  proposal,
  routerClient,
  targetTaskTitle,
  onApproved,
  onDismissed,
}: ProposalCardProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const kind = KIND_LABELS[proposal.type];
  const description = summarize(proposal);
  const headline = proposal.type === "create_task" ? proposal.task?.title : targetTaskTitle;

  if (proposal.status === "approved") {
    return (
      <Draft className={styles.applied}>
        <div className={styles.header}>
          <Chip tone="draft">{kind}</Chip>
          {headline && <span className={styles.title}>{headline}</span>}
        </div>
        <p className={styles.notice}>Applied automatically</p>
      </Draft>
    );
  }

  async function handleApprove() {
    setBusy(true);
    setError(null);
    try {
      const updated = await routerClient.approveProposal(proposal.id);
      onApproved?.(updated);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to approve proposal");
      setBusy(false);
    }
  }

  async function handleDismiss() {
    setBusy(true);
    setError(null);
    try {
      await routerClient.dismissProposal(proposal.id);
      onDismissed?.(proposal.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to dismiss proposal");
      setBusy(false);
    }
  }

  return (
    <Draft className={styles.card}>
      <div className={styles.header}>
        <Chip tone="draft">{kind}</Chip>
        {headline && <span className={styles.title}>{headline}</span>}
      </div>
      {description && <p className={styles.description}>{description}</p>}
      {error && <p className={styles.error}>{error}</p>}
      <div className={styles.actions}>
        <Button size="sm" onClick={handleApprove} disabled={busy}>
          Approve
        </Button>
        <Button variant="ghost" size="sm" onClick={handleDismiss} disabled={busy}>
          Dismiss
        </Button>
      </div>
    </Draft>
  );
}
