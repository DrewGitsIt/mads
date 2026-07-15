import { Card } from "../../components/Card/Card";
import { Markdown } from "../ChatRenderer/markdown";
import type { Session } from "../../router/types";
import styles from "./SessionSummaryCard.module.css";

export interface SessionSummaryCardProps {
  session: Session;
}

/**
 * Only renders for inactive sessions with a summary. `session.summary` is
 * already server-rendered markdown (pkg/sessionsummary) — this does no
 * section-presence logic, just hands it to the shared sanitized renderer.
 */
export function SessionSummaryCard({ session }: SessionSummaryCardProps) {
  if (session.activity !== "inactive" || !session.summary) return null;

  return (
    <Card className={styles.card}>
      <Markdown text={session.summary} className={styles.prose} />
    </Card>
  );
}
