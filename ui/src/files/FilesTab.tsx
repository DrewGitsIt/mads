import { useEffect, useState } from "react";
import { Chip } from "../components/Chip/Chip";
import { Panel, PanelBody, PanelHeader } from "../components/Panel/Panel";
import type { RouterClient } from "../router/client";
import type { Commit, FileChange, FileDiff, Session } from "../router/types";
import { ChangeList } from "./ChangeList/ChangeList";
import { CommitList } from "./ChangeList/CommitList";
import { DiffViewer } from "./DiffViewer/DiffViewer";
import { MergeBackFlow } from "./MergeBack/MergeBackFlow";
import styles from "./FilesTab.module.css";

export interface FilesTabProps {
  sessionId: string;
  taskId?: string;
  routerClient: RouterClient;
}

type Scope = "session" | "task";

/**
 * Files tab (MADS-UX §3.3/§5/§17, W6.5). Composes the change/commit lists,
 * the diff viewer, and the merge-back flow. `taskId` is optional so this
 * also works from a session-only caller; the session/task scope switch is
 * only interactive once a `taskId` is actually available (HANDOFF §2/§8).
 */
export function FilesTab({ sessionId, taskId, routerClient }: FilesTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [scope, setScope] = useState<Scope>("session");
  const [changes, setChanges] = useState<FileChange[]>([]);
  const [commits, setCommits] = useState<Commit[]>([]);
  const [taskTitles, setTaskTitles] = useState<Map<string, string>>(new Map());
  const [selectedPath, setSelectedPath] = useState<string | undefined>();
  const [diff, setDiff] = useState<FileDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState<string | undefined>();

  const scopedTaskId = scope === "task" ? taskId : undefined;

  useEffect(() => {
    let cancelled = false;
    void routerClient.getSession(sessionId).then((s) => {
      if (!cancelled) setSession(s);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId]);

  useEffect(() => {
    let cancelled = false;
    void routerClient.listTasks(sessionId).then((tasks) => {
      if (!cancelled) setTaskTitles(new Map(tasks.map((t) => [t.id, t.title])));
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId]);

  useEffect(() => {
    let cancelled = false;
    void routerClient.getFileChanges(sessionId, scopedTaskId).then((fc) => {
      if (!cancelled) setChanges(fc);
    });
    void routerClient.getCommits(sessionId).then((cs) => {
      if (!cancelled) {
        const scoped = scopedTaskId ? cs.filter((c) => c.task_id === scopedTaskId) : cs;
        setCommits(scoped.slice().reverse());
      }
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, scopedTaskId]);

  useEffect(() => {
    if (!selectedPath) {
      setDiff(null);
      setDiffError(undefined);
      return;
    }
    let cancelled = false;
    setDiffLoading(true);
    setDiffError(undefined);
    routerClient
      .getFileDiff(sessionId, selectedPath, scopedTaskId)
      .then((d) => {
        if (!cancelled) setDiff(d);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setDiff(null);
          setDiffError(String(err));
        }
      })
      .finally(() => {
        if (!cancelled) setDiffLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, selectedPath, scopedTaskId]);

  return (
    <div className={styles.tab}>
      <div className={styles.lists}>
        {taskId && (
          <div className={styles.scopeSwitch}>
            <Chip tone="neutral" pressed={scope === "session"} onClick={() => setScope("session")}>
              Whole session
            </Chip>
            <Chip tone="neutral" pressed={scope === "task"} onClick={() => setScope("task")}>
              This task
            </Chip>
          </div>
        )}

        {commits.length === 0 && changes.length === 0 ? (
          <Panel>
            <PanelBody>
              <div className={styles.emptyBoth}>
                No commits recorded and no working changes observed for this session yet.
              </div>
            </PanelBody>
          </Panel>
        ) : (
          <>
            {commits.length > 0 && (
              <Panel>
                <PanelHeader>Committed</PanelHeader>
                <PanelBody>
                  <CommitList commits={commits} taskTitles={taskTitles} />
                </PanelBody>
              </Panel>
            )}
            {changes.length > 0 && (
              <Panel>
                <PanelHeader>Working changes</PanelHeader>
                <PanelBody>
                  <ChangeList changes={changes} selectedPath={selectedPath} onSelect={setSelectedPath} />
                </PanelBody>
              </Panel>
            )}
          </>
        )}

        <MergeBackFlow
          sessionId={sessionId}
          routerClient={routerClient}
          worktreeDir={session?.worktree_dir}
        />
      </div>

      <DiffViewer
        path={selectedPath}
        diff={diff}
        loading={diffLoading}
        error={diffError}
        onOpenInEditor={(path) => void routerClient.openInEditor(path)}
      />
    </div>
  );
}
