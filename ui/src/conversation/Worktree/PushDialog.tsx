import { useEffect, useId, useState } from "react";
import { Button } from "../../components/Button/Button";
import type { RouterClient } from "../../router/client";
import type {
  PushCommit,
  PushPreview,
  PushResult,
  PushTarget,
  Session,
} from "../../router/types";
import {
  plural,
  pushRefusalCopy,
  rebasePrompt,
  remoteRef,
  repoName,
  shortHash,
} from "./worktreeCopy";
import shell from "./CommitDialog.module.css";
import styles from "./PushDialog.module.css";

export interface PushDialogProps {
  open: boolean;
  sessionId: string;
  routerClient: RouterClient;
  /** Its repos say which have commits to push (the picker, when more than one does). */
  session: Session;
  /** What to push in the session's own repo; any other repo pushes its branch. */
  target: PushTarget;
  onClose: () => void;
  /** The push went through — the caller says so (a toast). */
  onPushed: (result: PushResult) => void;
  /** Puts an editable message in the composer. Unset: no "Ask the agent to rebase". */
  onAskAgent?: (text: string) => void;
}

function cx(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

/**
 * Confirm a push (docs/COMMIT-LAND-PUSH-DESIGN.md D5, mockup
 * commit-land-push.html §3 "Push main to origin?"). Every push asks: the
 * dialog fetches the remote, lists what would go and what's coming in, and
 * pushes exactly the tip it showed (`expect`). Never a force push. When the
 * remote is ahead, the main button becomes "Rebase onto <remote> and push" —
 * the only way a rebase ever happens; if it conflicts, it's undone and the
 * agent can be asked to do it. One repo per confirmation: with several to
 * push, a picker at the top chooses which.
 */
export function PushDialog({
  open,
  sessionId,
  routerClient,
  session,
  target,
  onClose,
  onPushed,
  onAskAgent,
}: PushDialogProps) {
  const titleId = useId();
  const choices = (session.repos ?? []).filter(
    (r) => (r.unpushed_count ?? 0) > 0,
  );
  const [repo, setRepo] = useState<string | undefined>();
  const [preview, setPreview] = useState<PushPreview | null>(null);
  const [loadError, setLoadError] = useState<string>();
  // Bumped to fetch the preview again (the branch moved under it).
  const [reload, setReload] = useState(0);
  const [pushing, setPushing] = useState(false);
  const [result, setResult] = useState<PushResult>();
  const [notice, setNotice] = useState<string>();

  // The picker starts on the session's own repo.
  const firstChoice = (choices.find((r) => r.primary) ?? choices[0])?.root;
  useEffect(() => {
    if (!open) return;
    setRepo(choices.length > 1 ? firstChoice : undefined);
    setResult(undefined);
    setNotice(undefined);
    setPushing(false);
    // Only on opening: a record refetch mustn't move the picker.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // The session's own (or only) repo takes the asked-for target; another
  // repo in the picker works in place, so it pushes its branch.
  const chosen = choices.find((r) => r.root === repo);
  const pushTarget: PushTarget =
    !chosen || chosen.primary || chosen.isolated ? target : "branch";

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setPreview(null);
    setLoadError(undefined);
    routerClient
      .getPushPreview(sessionId, { repo, target: pushTarget })
      .then((p) => {
        if (!cancelled) setPreview(p);
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setLoadError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [open, sessionId, routerClient, repo, pushTarget, reload]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  const remote = preview?.remote;
  const upstream = remoteRef(remote, preview?.remote_branch);
  const behind = preview ? preview.behind > 0 : false;

  async function push() {
    if (!preview) return;
    setPushing(true);
    setResult(undefined);
    setNotice(undefined);
    try {
      const res = await routerClient.pushSession(sessionId, {
        repo,
        target: pushTarget,
        rebase: behind || undefined,
        expect: preview.head,
      });
      if (res.outcome === "pushed") {
        onPushed(res);
        onClose();
        return;
      }
      if (
        res.outcome === "refused" &&
        (res.reason === "moved" || res.reason === "behind")
      ) {
        // What the confirmation listed is stale: show what's true now.
        setNotice(pushRefusalCopy(res.reason));
        setReload((n) => n + 1);
        return;
      }
      setResult(res);
    } catch (err) {
      setResult({
        outcome: "refused",
        detail: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setPushing(false);
    }
  }

  const title = preview
    ? `Push ${preview.branch ?? "this branch"}${remote ? ` to ${remote}` : ""}${preview.new_branch ? " as a new branch" : ""}?`
    : "Push this session's work?";
  const blocked = preview?.blocked;
  const rebaseBlocked = behind ? preview?.rebase_blocked : undefined;
  const confirmLabel = pushing
    ? behind
      ? "Rebasing and pushing…"
      : "Pushing…"
    : behind
      ? `Rebase onto ${remote ?? "the remote"} and push`
      : "Push";

  return (
    <div className={shell.backdrop} onMouseDown={onClose}>
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={cx(shell.dialog, styles.dialog)}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className={shell.head}>
          <h2 id={titleId} className={shell.title}>
            {title}
          </h2>
          {preview?.new_branch && (
            <p className={shell.subtitle}>
              {remote} has no {preview.branch} yet: the push creates it and
              tracks it.
            </p>
          )}
        </div>
        <div className={shell.main}>
          {choices.length > 1 && (
            <div
              className={styles.repos}
              role="radiogroup"
              aria-label="Repo to push"
            >
              {choices.map((r) => (
                <label key={r.root} className={styles.repo} title={r.root}>
                  <input
                    type="radio"
                    name={`${titleId}-repo`}
                    checked={r.root === repo}
                    disabled={pushing}
                    onChange={() => setRepo(r.root)}
                  />
                  {repoName(r.root)}
                  <span className={styles.ahead}>↑{r.unpushed_count}</span>
                </label>
              ))}
            </div>
          )}
          {loadError ? (
            <div className={shell.error}>
              Couldn't check the remote: {loadError}
            </div>
          ) : !preview ? (
            <div className={styles.loading} role="status">
              <span className={styles.spinner} aria-hidden="true" />
              Fetching {remote ?? "the remote"}…
            </div>
          ) : (
            <>
              {notice && <div className={styles.notice}>{notice}</div>}
              {preview.commits.length > 0 && (
                <div>
                  <div className={styles.listHead}>
                    {plural(preview.ahead, "commit")}:
                  </div>
                  <CommitList
                    commits={preview.commits}
                    total={preview.ahead}
                    label="Commits to push"
                  />
                </div>
              )}
              {preview.fetch_error ? (
                <div className={shell.error}>
                  Couldn't fetch {remote ?? "the remote"}: {preview.fetch_error}
                </div>
              ) : (
                <div className={shell.muted}>
                  {remote ? `${upstream} fetched just now` : "fetched just now"}
                  {behind ? "" : " · fast-forward"} · never forced
                </div>
              )}
              {blocked && (
                <div className={styles.blocked} data-reason={blocked.reason}>
                  {blocked.detail ?? "Push can't run here."}
                </div>
              )}
              {!blocked && behind && (
                <div>
                  <div className={styles.listHead}>
                    {upstream} has {plural(preview.behind, "commit")} you don't
                    have
                  </div>
                  <CommitList
                    commits={preview.incoming}
                    total={preview.behind}
                    label="Incoming commits"
                  />
                  {rebaseBlocked && (
                    <div
                      className={styles.blocked}
                      data-reason={rebaseBlocked.reason}
                    >
                      {rebaseBlocked.detail ?? "It can't be rebased here."}
                      {rebaseBlocked.files && rebaseBlocked.files.length > 0 && (
                        <ul className={styles.files}>
                          {rebaseBlocked.files.map((f) => (
                            <li key={f}>{f}</li>
                          ))}
                        </ul>
                      )}
                    </div>
                  )}
                </div>
              )}
              {result && (
                <Outcome
                  result={result}
                  branch={preview.branch}
                  upstream={upstream}
                  onAskAgent={
                    onAskAgent &&
                    ((text) => {
                      onAskAgent(text);
                      onClose();
                    })
                  }
                />
              )}
            </>
          )}
        </div>
        <div className={shell.foot}>
          <span className={shell.spacer} />
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          {!blocked && !loadError && (
            <Button
              variant="primary"
              disabled={!preview || pushing || Boolean(rebaseBlocked)}
              title={rebaseBlocked?.detail}
              onClick={() => void push()}
            >
              {confirmLabel}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

/** Short hash and subject, newest first; "and N more" past what the Router listed. */
function CommitList({
  commits,
  total,
  label,
}: {
  commits: PushCommit[];
  total: number;
  label: string;
}) {
  return (
    <ul className={styles.commits} aria-label={label}>
      {commits.map((c) => (
        <li key={c.hash}>
          <span className={shell.hash}>{shortHash(c.hash)}</span> {c.subject}
        </li>
      ))}
      {total > commits.length && (
        <li className={shell.muted}>and {total - commits.length} more</li>
      )}
    </ul>
  );
}

/** A push that didn't go: refused (git's words verbatim where it spoke), or a rebase that conflicted and was undone. */
function Outcome({
  result,
  branch,
  upstream,
  onAskAgent,
}: {
  result: PushResult;
  branch: string | undefined;
  upstream: string;
  onAskAgent?: (text: string) => void;
}) {
  if (result.outcome === "conflicted") {
    const files = result.files ?? [];
    return (
      <div className={shell.errorBlock} role="alert" data-outcome="conflicted">
        <div className={shell.error}>
          Rebasing onto {upstream} hit conflicts
          {files.length > 0 ? ` in ${plural(files.length, "file")}` : ""}, so
          it was undone. Nothing was pushed.
        </div>
        {files.length > 0 && (
          <ul className={styles.files}>
            {files.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        )}
        {onAskAgent && (
          <div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onAskAgent(rebasePrompt(branch, upstream, files))}
            >
              Ask the agent to rebase
            </Button>
          </div>
        )}
      </div>
    );
  }
  const verbatim =
    result.reason === "push_failed" || result.reason === "rebase_failed";
  return (
    <div
      className={shell.errorBlock}
      role="alert"
      data-outcome="refused"
      data-reason={result.reason}
    >
      <div className={shell.error}>{pushRefusalCopy(result.reason)}</div>
      {result.detail &&
        (verbatim ? (
          <pre className={shell.detail}>{result.detail}</pre>
        ) : (
          <div className={shell.muted}>{result.detail}</div>
        ))}
    </div>
  );
}
