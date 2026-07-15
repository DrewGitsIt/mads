import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { routerPost } from "../../router/client";
import type { Session, Task } from "../../router/types";
import styles from "./FollowUpCta.module.css";

export interface FollowUpCtaProps {
  retroId: string;
  acceptedCount: number;
  followUpSessionId?: string;
  onCreated?: (result: { session: Session; tasks: Task[] }) => void;
}

/**
 * "Create follow-up session (N accepted)" CTA, or a created-indicator once
 * retro.follow_up_session_id is already set.
 */
export function FollowUpCta({
  retroId,
  acceptedCount,
  followUpSessionId,
  onCreated,
}: FollowUpCtaProps) {
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (followUpSessionId) {
    return (
      <div className={styles.created} role="status">
        Follow-up session created
      </div>
    );
  }

  const create = async () => {
    setCreating(true);
    setError(null);
    try {
      const result = await routerPost<{ session: Session; addr: string; tasks: Task[] }>(
        `/retros/${retroId}/follow-up-session`,
      );
      onCreated?.({ session: result.session, tasks: result.tasks });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className={styles.cta}>
      <Button
        variant="primary"
        onClick={() => void create()}
        disabled={acceptedCount === 0 || creating}
      >
        Create follow-up session ({acceptedCount} accepted)
      </Button>
      {error && <Toast tone="bad" title="Couldn't create follow-up session" description={error} />}
    </div>
  );
}
