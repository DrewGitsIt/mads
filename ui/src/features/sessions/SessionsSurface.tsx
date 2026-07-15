import { useState } from "react";
import { Toast } from "../../components/Toast/Toast";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionsList } from "./state/useSessionsList";
import styles from "./SessionsSurface.module.css";

/** Top-level Sessions surface (registered in App.tsx) — owns the shared session list so the rail and launchpad don't duplicate fetch/subscribe logic. */
export function SessionsSurface() {
  const { sessions, loading, error, createSession } = useSessionsList();
  const openTaskCounts = useOpenTaskCounts();
  const [selectedId, setSelectedId] = useState<string>();

  return (
    <div className={styles.surface}>
      <aside className={styles.rail}>
        {!loading && sessions.length === 0 ? (
          <NoSessionsEmptyState />
        ) : (
          <SessionRail
            sessions={sessions}
            openTaskCounts={openTaskCounts}
            selectedId={selectedId}
            onSelect={setSelectedId}
          />
        )}
      </aside>
      <main className={styles.main}>
        <Launchpad createSession={createSession} />
        {error && <Toast tone="bad" title="Couldn't load sessions" description={error} />}
      </main>
    </div>
  );
}
