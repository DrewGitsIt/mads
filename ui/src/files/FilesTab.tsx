import { useEffect, useState } from "react";
import { Chip } from "../components/Chip/Chip";
import { Panel, PanelBody, PanelHeader } from "../components/Panel/Panel";
import type { RouterClient } from "../router/client";
import type { Commit, FileChange, FileDiff, Session } from "../router/types";
import { collapseChanges } from "../context-rail/collapseChanges";
import { displayPath } from "./displayPath";
import { ChangeList } from "./ChangeList/ChangeList";
import { CommitList } from "./ChangeList/CommitList";
import { DiffViewer } from "./DiffViewer/DiffViewer";
import { MergeBackFlow } from "./MergeBack/MergeBackFlow";
import styles from "./FilesTab.module.css";

export interface FilesTabProps {
  sessionId: string;
  taskId?: string;
  routerClient: RouterClient;
  /** Path to auto-select in the diff viewer — set when opened from the Context Rail's Changes list. */
  focusPath?: string;
}

type Scope = "session" | "task";

type Selection = { source: "working"; path: string } | { source: "commit"; hash: string; path: string };

/**
 * Files tab (MADS-UX §3.3/§5/§17, W6.5). Composes the change/commit lists,
 * the diff viewer, and the merge-back flow. `taskId` is optional so this
 * also works from a session-only caller; the session/task scope switch is
 * only interactive once a `taskId` is actually available (HANDOFF §2/§8).
 */
export function FilesTab({ sessionId, taskId, routerClient, focusPath }: FilesTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [scope, setScope] = useState<Scope>("session");
  const [changes, setChanges] = useState<FileChange[]>([]);
  const [commits, setCommits] = useState<Commit[]>([]);
  const [taskTitles, setTaskTitles] = useState<Map<string, string>>(new Map());
  const [selection, setSelection] = useState<Selection | null>(null);
  const [diff, setDiff] = useState<FileDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState<string | undefined>();

  const scopedTaskId = scope === "task" ? taskId : undefined;

  // getFileChanges returns the append-only touch ledger (one row per
  // observation), so a file edited many times would render many rows here.
  // Collapse to one row per file with its net status, as the rail does.
  const workingChanges = collapseChanges(changes);

  // Opened from the rail — select the working-change diff for that file. Keyed
  // on focusPath so re-opening a different file re-selects; the rail always
  // fetches session-scoped changes, so drop to session scope to match.
  useEffect(() => {
    if (!focusPath) return;
    setScope("session");
    setSelection({ source: "working", path: focusPath });
  }, [focusPath]);

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
    if (!selection) {
      setDiff(null);
      setDiffError(undefined);
      return;
    }
    let cancelled = false;
    setDiffLoading(true);
    setDiffError(undefined);
    const request =
      selection.source === "working"
        ? routerClient.getFileDiff(sessionId, selection.path, scopedTaskId)
        : routerClient.getCommitFileDiff(sessionId, selection.hash, selection.path);
    request
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
  }, [routerClient, sessionId, selection, scopedTaskId]);

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

        {commits.length === 0 && workingChanges.length === 0 ? (
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
                <PanelHeader>Committed ({commits.length})</PanelHeader>
                <PanelBody>
                  <CommitList
                    commits={commits}
                    taskTitles={taskTitles}
                    selectedFile={
                      selection?.source === "commit"
                        ? { hash: selection.hash, path: selection.path }
                        : undefined
                    }
                    onSelectFile={(hash, path) => setSelection({ source: "commit", hash, path })}
                  />
                </PanelBody>
              </Panel>
            )}
            {workingChanges.length > 0 && (
              <Panel>
                <PanelHeader>Working changes ({workingChanges.length})</PanelHeader>
                <PanelBody>
                  <ChangeList
                    changes={workingChanges}
                    selectedPath={selection?.source === "working" ? selection.path : undefined}
                    onSelect={(path) => setSelection({ source: "working", path })}
                    sessionId={sessionId}
                    worktreeDir={session?.worktree_dir}
                  />
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
        path={selection?.path}
        pathLabel={selection ? displayPath(selection.path, session?.worktree_dir) : undefined}
        commitHash={selection?.source === "commit" ? selection.hash : undefined}
        diff={diff}
        loading={diffLoading}
        error={diffError}
        onOpenInEditor={(path) => void routerClient.openInEditor(path)}
      />
    </div>
  );
}
