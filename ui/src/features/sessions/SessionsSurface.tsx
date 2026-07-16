import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { ConversationTab } from "../../conversation/ConversationTab";
import { useElectronPtyBridge } from "../../conversation/TerminalRenderer/ptyBridge";
import { router } from "../../router/client";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionsList } from "./state/useSessionsList";
import styles from "./SessionsSurface.module.css";

export interface SessionsSurfaceProps {
  jumpToSessionId?: string;
}

/** Top-level Sessions surface (registered in App.tsx) — owns the shared session list so the rail and launchpad don't duplicate fetch/subscribe logic. */
export function SessionsSurface({ jumpToSessionId }: SessionsSurfaceProps = {}) {
  const { sessions, loading, error, createSession } = useSessionsList();
  const openTaskCounts = useOpenTaskCounts();
  const [selectedId, setSelectedId] = useState<string>();
  const ptyBridge = useElectronPtyBridge();

  useEffect(() => {
    if (jumpToSessionId) setSelectedId(jumpToSessionId);
  }, [jumpToSessionId]);

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
            onSelect={(id) => setSelectedId((cur) => (cur === id ? undefined : id))}
          />
        )}
      </aside>
      <main className={styles.main}>
        {selectedId ? (
          <div className={styles.conversationPane}>
            <div className={styles.paneHeader}>
              <Button
                variant="ghost"
                size="sm"
                aria-label="Back to Launchpad"
                onClick={() => setSelectedId(undefined)}
              >
                ✕
              </Button>
            </div>
            <ConversationTab
              key={selectedId}
              sessionId={selectedId}
              routerClient={router}
              ptyBridge={ptyBridge}
            />
          </div>
        ) : (
          <Launchpad createSession={createSession} />
        )}
        {error && <Toast tone="bad" title="Couldn't load sessions" description={error} />}
      </main>
    </div>
  );
}
