import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import { Panel, PanelBody, PanelHeader } from "../../components/Panel/Panel";
import { MergeEditor } from "./MergeEditor";
import styles from "./ConflictResolution.module.css";

export interface ConflictResolutionProps {
  branch?: string;
  files: string[];
  worktreeDir?: string;
  onOpenInEditor: (path: string) => void;
  onRetry: () => void;
  conflictPreview?: { path: string; text: string };
}

/**
 * The real, usable part of conflict resolution (HANDOFF §5): the merge
 * implementation aborts on conflict, so there's no live conflicted worktree
 * to point an editor at — only the list of paths that would have
 * conflicted and the still-existing branch name. This renders that list
 * plus an external-editor handoff and a retry action; the embedded
 * MergeEditor below it is a seed-only stub, not wired to real content.
 */
export function ConflictResolution({
  branch,
  files,
  worktreeDir,
  onOpenInEditor,
  onRetry,
  conflictPreview,
}: ConflictResolutionProps) {
  return (
    <Panel className={styles.panel}>
      <PanelHeader>
        <Chip tone="bad">Merge conflicted</Chip>
        {branch && <span className={styles.branch}>{branch}</span>}
      </PanelHeader>
      <PanelBody>
        <ul className={styles.fileList}>
          {files.map((path) => (
            <li key={path} className={styles.file}>
              {path}
            </li>
          ))}
        </ul>

        <div className={styles.actions}>
          <Button
            variant="secondary"
            size="sm"
            disabled={!worktreeDir}
            onClick={() => worktreeDir && onOpenInEditor(worktreeDir)}
          >
            Open worktree in external editor
          </Button>
          <Button variant="primary" size="sm" onClick={onRetry}>
            Retry merge
          </Button>
        </div>
        {!worktreeDir && (
          <div className={styles.hint}>
            No worktree path available for this session yet.
          </div>
        )}

        {conflictPreview && (
          <div className={styles.previewWrap}>
            <MergeEditor
              path={conflictPreview.path}
              conflictText={conflictPreview.text}
              onResolve={() => {}}
            />
          </div>
        )}
      </PanelBody>
    </Panel>
  );
}
