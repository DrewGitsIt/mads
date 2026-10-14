import { useEffect, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog/ConfirmDialog";
import { CommitRefusedError, type RouterClient } from "../../router/client";
import type { CommitError, CommitPath, CommitResult, WorktreeStatus } from "../../router/types";
import { commitPathReason, plural, shortHash } from "./worktreeCopy";
import styles from "./CommitDialog.module.css";

export interface CommitDialogProps {
  open: boolean;
  sessionId: string;
  routerClient: RouterClient;
  /** A Fast Model can draft — shows "Draft with Fast Model". */
  fastModel: boolean;
  /** A refusal to show on open — the one-click Commit falling back to the dialog. */
  initialError?: CommitError;
  onClose: () => void;
  onCommitted: (result: CommitResult) => void;
  /** Opens a file's diff in the Changes tab (absolute path, as the change list records it). */
  onOpenFile?: (path: string) => void;
}

const KIND_CLASS: Record<string, string> = {
  added: styles.kindAdded,
  modified: styles.kindModified,
  deleted: styles.kindDeleted,
};

function joinPath(folder: string | undefined, path: string): string {
  if (!folder) return path;
  return `${folder.replace(/\/+$/, "")}/${path}`;
}

/**
 * Commit this session's changes (D5, mockup §2). Lists only the session's
 * own uncommitted files — its change list as git sees it, fetched when the
 * dialog opens, never per render. A refusal renders in place and keeps the
 * selection, so a failing hook can be fixed and the same commit retried.
 */
export function CommitDialog({
  open,
  sessionId,
  routerClient,
  fastModel,
  initialError,
  onClose,
  onCommitted,
  onOpenFile,
}: CommitDialogProps) {
  const [status, setStatus] = useState<WorktreeStatus | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const [message, setMessage] = useState("");
  const [drafting, setDrafting] = useState(false);
  const [draftNote, setDraftNote] = useState<string>();
  const [committing, setCommitting] = useState(false);
  const [error, setError] = useState<CommitError | undefined>(initialError);
  // Per-path outcomes the server named: a refusal's, or what a partial
  // commit skipped.
  const [notes, setNotes] = useState<CommitPath[]>([]);
  const [partial, setPartial] = useState<CommitResult>();

  const load = (keep?: string[]) =>
    routerClient
      .getWorktreeStatus(sessionId)
      .then((st) => {
        setStatus(st);
        setLoadError(undefined);
        const listed = st.files.map((f) => f.path);
        setChecked(new Set(keep ? listed.filter((p) => keep.includes(p)) : listed));
      })
      .catch((err: unknown) => setLoadError(err instanceof Error ? err.message : String(err)));

  useEffect(() => {
    if (!open) return;
    setStatus(null);
    setMessage("");
    setDraftNote(undefined);
    setNotes(initialError?.paths ?? []);
    setPartial(undefined);
    setError(initialError);
    void load(initialError?.selection?.length ? initialError.selection : undefined);
    // load is a fresh closure every render; opening is the only trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, sessionId, routerClient, initialError]);

  const files = status?.files ?? [];
  const selected = files.filter((f) => checked.has(f.path)).map((f) => f.path);
  const noteFor = new Map(notes.map((n) => [n.path, commitPathReason(n.status, n.reason)]));

  function toggle(path: string) {
    setChecked((cur) => {
      const next = new Set(cur);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }

  async function draft() {
    setDrafting(true);
    setDraftNote(undefined);
    try {
      const d = await routerClient.draftCommitMessage(sessionId, selected);
      setMessage(d.message);
      if (d.truncated) setDraftNote("The diff was too long to send whole, so this draft saw only part of it.");
    } catch (err) {
      setDraftNote(err instanceof CommitRefusedError ? err.body.error : "The Fast Model couldn't draft a message.");
    } finally {
      setDrafting(false);
    }
  }

  async function commit() {
    setCommitting(true);
    setError(undefined);
    setNotes([]);
    setPartial(undefined);
    try {
      const res = await routerClient.commitSession(sessionId, { paths: selected, message: message.trim() });
      onCommitted(res);
      const skipped = res.paths.filter((p) => p.status !== "committed");
      if (skipped.length === 0) {
        onClose();
        return;
      }
      // Committed, but not everything asked for: say which and why.
      setPartial(res);
      setNotes(skipped);
      setMessage("");
      void load();
    } catch (err) {
      if (err instanceof CommitRefusedError) {
        setError(err.body);
        setNotes(err.body.paths ?? []);
        if (err.body.code === "nothing_to_commit") void load();
      } else {
        setError({ code: "unexpected", error: err instanceof Error ? err.message : String(err) });
      }
    } finally {
      setCommitting(false);
    }
  }

  const listedNotes = notes.filter((n) => !files.some((f) => f.path === n.path));

  return (
    <ConfirmDialog
      open={open}
      title="Commit this session's changes"
      confirmTone="primary"
      confirmLabel={committing ? "Committing…" : `Commit ${plural(selected.length, "file")}`}
      confirmDisabled={committing || selected.length === 0 || message.trim() === ""}
      cancelLabel={partial ? "Done" : "Cancel"}
      onConfirm={() => void commit()}
      onCancel={onClose}
      description={
        <div className={styles.body}>
          {partial && (
            <div className={styles.partial}>
              Committed {plural(partial.paths.filter((p) => p.status === "committed").length, "file")} as{" "}
              <span className={styles.hash}>{shortHash(partial.hash)}</span>.
            </div>
          )}
          {loadError ? (
            <div className={styles.error}>Couldn't read this session's changes: {loadError}</div>
          ) : !status ? (
            <div className={styles.muted}>Reading this session's changes…</div>
          ) : files.length === 0 ? (
            <div className={styles.muted}>Nothing left to commit: every file this session changed is committed.</div>
          ) : (
            <div className={styles.files} role="group" aria-label="Files to commit">
              {files.map((f) => {
                const on = checked.has(f.path);
                const note = noteFor.get(f.path);
                return (
                  <div key={f.path} className={[styles.file, on ? "" : styles.off].filter(Boolean).join(" ")}>
                    <input
                      type="checkbox"
                      checked={on}
                      onChange={() => toggle(f.path)}
                      aria-label={`Include ${f.path}`}
                    />
                    {onOpenFile ? (
                      <button
                        type="button"
                        className={styles.path}
                        title="Open its diff"
                        onClick={() => onOpenFile(joinPath(status.folder, f.path))}
                      >
                        {f.path}
                      </button>
                    ) : (
                      <span className={styles.path}>{f.path}</span>
                    )}
                    {note && <span className={styles.note}>{note}</span>}
                    <span className={[styles.kind, KIND_CLASS[f.kind] ?? ""].join(" ")}>{f.kind}</span>
                  </div>
                );
              })}
            </div>
          )}
          {listedNotes.length > 0 && (
            <ul className={styles.notes}>
              {listedNotes.map((n) => (
                <li key={n.path}>
                  <span className={styles.mono}>{n.path}</span> — {commitPathReason(n.status, n.reason)}
                </li>
              ))}
            </ul>
          )}
          <textarea
            className={styles.msg}
            value={message}
            placeholder="What does this change do, and why?"
            aria-label="Commit message"
            onChange={(e) => setMessage(e.target.value)}
          />
          <div className={styles.msgbar}>
            {fastModel ? (
              <button
                type="button"
                className={styles.linkbtn}
                disabled={drafting || selected.length === 0}
                onClick={() => void draft()}
              >
                {drafting ? "Drafting…" : "Draft with Fast Model"}
              </button>
            ) : (
              <span />
            )}
            <span className={styles.muted}>
              {status?.branch ? (
                <>
                  on <span className={styles.mono}>{status.branch}</span> · as you
                </>
              ) : (
                "as you"
              )}
            </span>
          </div>
          {draftNote && <div className={styles.muted}>{draftNote}</div>}
          {error && (
            <div className={styles.errorBlock} role="alert">
              <div className={styles.error}>{error.error}</div>
              {error.detail && <pre className={styles.detail}>{error.detail}</pre>}
            </div>
          )}
        </div>
      }
    />
  );
}
