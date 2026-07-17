import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { useElectronPtyBridge } from "../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { router } from "../../router/client";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionsList } from "./state/useSessionsList";
import { SessionWorkspace } from "./workspace/SessionWorkspace";
import type { WorkspaceTabId } from "./workspace/tabRegistry";
import styles from "./SessionsSurface.module.css";

export interface SessionsSurfaceProps {
  jumpToSessionId?: string;
  jumpToTab?: WorkspaceTabId;
  pipelineClient: PipelineClient;
}

/** Top-level Sessions surface (registered in App.tsx) — owns the shared session list so the rail and launchpad don't duplicate fetch/subscribe logic. */
export function SessionsSurface({ jumpToSessionId, jumpToTab, pipelineClient }: SessionsSurfaceProps) {
  const { sessions, loading, error, createSession } = useSessionsList();
  const openTaskCounts = useOpenTaskCounts();
  const [selectedId, setSelectedId] = useState<string>();
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [resumeError, setResumeError] = useState<string>();
  const ptyBridge = useElectronPtyBridge();

  useEffect(() => {
    if (jumpToSessionId) {
      setSelectedId(jumpToSessionId);
      setInitialTab(jumpToTab);
    }
  }, [jumpToSessionId, jumpToTab]);

  return (
    <div className={styles.surface}>
      <aside className={styles.rail}>
        <Button variant="ghost" size="sm" onClick={() => setSelectedId(undefined)}>
          + New session
        </Button>
        {!loading && sessions.length === 0 ? (
          <NoSessionsEmptyState />
        ) : (
          <SessionRail
            sessions={sessions}
            openTaskCounts={openTaskCounts}
            selectedId={selectedId}
            onSelect={(id) => {
              setSelectedId((cur) => (cur === id ? undefined : id));
              setInitialTab(undefined);
            }}
            onResume={(id) => {
              router.resumeSession(id).catch((err: unknown) => {
                setResumeError(err instanceof Error ? err.message : String(err));
              });
            }}
          />
        )}
      </aside>
      <main className={styles.main}>
        {selectedId ? (
          <SessionWorkspace
            key={selectedId}
            sessionId={selectedId}
            routerClient={router}
            pipelineClient={pipelineClient}
            ptyBridge={ptyBridge}
            openTaskCount={openTaskCounts[selectedId] ?? 0}
            initialTab={initialTab}
            onClose={() => setSelectedId(undefined)}
          />
        ) : (
          <Launchpad createSession={createSession} />
        )}
        {error && <Toast tone="bad" title="Couldn't load sessions" description={error} />}
        {resumeError && (
          <Toast tone="bad" title="Couldn't resume session" description={resumeError} />
        )}
      </main>
    </div>
  );
}
