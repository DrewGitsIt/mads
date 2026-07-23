import { useState } from "react";
import type { RouterClient } from "../router/client";
import type { Proposal, Task } from "../router/types";
import { ProposalCard } from "./ProposalCard";
import styles from "./RailProposalList.module.css";

export interface RailProposalListProps {
  proposals: Proposal[];
  tasksById: Record<string, Task>;
  routerClient: RouterClient;
}

/**
 * Renders synthesis proposals above the rail's task/change tabs (spec §4) —
 * visible regardless of which rail tab is active, so a proposal can't be
 * missed behind a tab switch. Dismissed proposals never render. A manually
 * approved/dismissed proposal is hidden immediately (optimistic,
 * `locallyResolvedIds`) rather than left to render ProposalCard's
 * already-applied notice — the resulting task/note change shows up in the
 * rail's own task list via the existing task.* event subscription, so
 * showing a lingering notice here would be a duplicate. Proposals that
 * arrive already `approved` (auto-approve config) were never seen pending by
 * this client, so they're not in `locallyResolvedIds` and do render the
 * brief notice.
 */
export function RailProposalList({ proposals, tasksById, routerClient }: RailProposalListProps) {
  const [locallyResolvedIds, setLocallyResolvedIds] = useState<Set<string>>(new Set());

  const visible = proposals.filter(
    (p) => p.status !== "dismissed" && !locallyResolvedIds.has(p.id),
  );

  if (visible.length === 0) return null;

  function resolve(id: string) {
    setLocallyResolvedIds((prev) => new Set(prev).add(id));
  }

  return (
    <div className={styles.list}>
      {visible.map((proposal) => (
        <ProposalCard
          key={proposal.id}
          proposal={proposal}
          routerClient={routerClient}
          targetTaskTitle={proposal.task_id ? tasksById[proposal.task_id]?.title : undefined}
          onApproved={() => resolve(proposal.id)}
          onDismissed={() => resolve(proposal.id)}
        />
      ))}
    </div>
  );
}
