import { Card } from "../../../components/Card/Card";
import { Chip, type ChipTone } from "../../../components/Chip/Chip";
import { Draft } from "../../../components/Draft/Draft";
import type { DPA, DPAStatus } from "../../../router/types";
import styles from "./DPACard.module.css";

export interface DPACardProps {
  dpa: DPA;
  onToggle: (dpaId: string) => void;
}

const STATUS_LABEL: Record<DPAStatus, string> = {
  accepted: "Accepted",
  rejected: "Rejected",
  edited: "Edited",
};

const STATUS_TONE: Record<DPAStatus, ChipTone> = {
  accepted: "ok",
  rejected: "bad",
  edited: "info",
};

/** DPA fields + a status Chip that IS the accept/reject toggle, wrapped in <Draft>. */
export function DPACard({ dpa, onToggle }: DPACardProps) {
  return (
    <Draft>
      <Card className={styles.card}>
        <div className={styles.header}>
          <span className={styles.layer}>{dpa.remediation_layer}</span>
          <Chip
            tone={STATUS_TONE[dpa.status]}
            pressed={dpa.status === "accepted"}
            onClick={() => onToggle(dpa.id)}
          >
            {STATUS_LABEL[dpa.status]}
          </Chip>
        </div>
        <p className={styles.problem}>{dpa.problem}</p>
        <dl className={styles.details}>
          <dt>Evidence</dt>
          <dd>{dpa.evidence}</dd>
          <dt>Root cause</dt>
          <dd>{dpa.root_cause}</dd>
          <dt>Suggested action</dt>
          <dd>{dpa.suggested_action}</dd>
        </dl>
      </Card>
    </Draft>
  );
}
