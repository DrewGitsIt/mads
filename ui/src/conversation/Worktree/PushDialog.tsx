import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Button } from "../../components/Button/Button";
import type { RouterClient } from "../../router/client";
import type {
  PushCommit,
  PushPreview,
  PushResult,
  PushTarget,
  Session,
  SessionRepo,
} from "../../router/types";
import { displayPath } from "../../files/displayPath";
import {
  plural,
  pushedCopy,
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
  /** Its repos say which have commits to push (a repo list, when more than one does). */
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

/** The push confirmation's repos: those the record says have commits to push. */
function pushableRepos(session: Session): SessionRepo[] {
  return (session.repos ?? []).filter((r) => (r.unpushed_count ?? 0) > 0);
}

/**
 * Confirm a push (docs/COMMIT-LAND-PUSH-DESIGN.md D5, mockup
 * commit-land-push.html §3 "Push main to origin?"). Every push asks: the
 * dialog fetches the remote, lists what would go and what's coming in, and
 * pushes exactly the tip it showed (`expect`). Never a force push. When the
 * remote is ahead, a rebase happens only when the user asks for it there;
 * if it conflicts, it's undone and the agent can be asked to do it. One repo
 * gets a single pane whose main button becomes "Rebase onto <remote> and
 * push"; several get a repo list, each repo with its own opt-in, pushed one
 * at a time (D5 "Several repos").
 */
export function PushDialog(props: PushDialogProps) {
  const { open, session, onClose } = props;
  // The layout is chosen as the dialog opens: a record refetch after the
  // first push mustn't fold a repo list into a single pane mid-run.
  const [openedWith, setOpenedWith] = useState<SessionRepo[] | null>(null);
  if (open && openedWith === null) setOpenedWith(pushableRepos(session));
  if (!open && openedWith !== null) setOpenedWith(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open || openedWith === null) return null;
  return openedWith.length > 1 ? (
    <MultiPush {...props} repos={openedWith} />
  ) : (
    <SinglePush {...props} />
  );
}

/** One repo: today's confirmation, its main button the push (or the rebase and push). */
function SinglePush({
  sessionId,
  routerClient,
  target,
  onClose,
  onPushed,
  onAskAgent,
}: PushDialogProps) {
  const titleId = useId();
  const [preview, setPreview] = useState<PushPreview | null>(null);
  const [loadError, setLoadError] = useState<string>();
  // Bumped to fetch the preview again (the branch moved under it).
  const [reload, setReload] = useState(0);
  const [pushing, setPushing] = useState(false);
  const [result, setResult] = useState<PushResult>();
  const [notice, setNotice] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    setPreview(null);
    setLoadError(undefined);
    routerClient
      .getPushPreview(sessionId, { repo: undefined, target })
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
  }, [sessionId, routerClient, target, reload]);

  const remote = preview?.remote;
  const behind = preview ? preview.behind > 0 : false;

  async function push() {
    if (!preview) return;
    setPushing(true);
    setResult(undefined);
    setNotice(undefined);
    try {
      const res = await routerClient.pushSession(sessionId, {
        target,
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

  const title = preview ? pushTitle(preview) : "Push this session's work?";
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
            <p className={shell.subtitle}>{newBranchNote(preview)}</p>
          )}
        </div>
        <div className={shell.main}>
          {loadError ? (
            <div className={shell.error}>
              Couldn't check the remote: {loadError}
            </div>
          ) : !preview ? (
            <Fetching remote={remote} />
          ) : (
            <PreviewBody
              preview={preview}
              notice={notice}
              result={result}
              onAskAgent={
                onAskAgent &&
                ((text) => {
                  onAskAgent(text);
                  onClose();
                })
              }
            />
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

/** One repo's half of the multi-repo dialog: its preview, its rebase opt-in, and how its push went. */
interface RepoPush {
  target: PushTarget;
  preview?: PushPreview;
  loadError?: string;
  /** The user ticked "Rebase onto … first" (behind only). */
  rebase: boolean;
  pushing: boolean;
  /** The last push that didn't go: refused or conflicted. */
  result?: PushResult;
  /** It went. */
  pushed?: PushResult;
  /** Why the preview was fetched again (the branch moved, the remote moved on). */
  notice?: string;
}

/** The repo list's dot: ready (green), behind without the opt-in (amber), can't push (grey), pushed (green ✓), failed (red). */
type PushStatus = "loading" | "ready" | "need" | "blocked" | "pushed" | "failed";

function pushStatus(r: RepoPush | undefined): PushStatus {
  if (!r) return "loading";
  if (r.pushed) return "pushed";
  if (r.loadError || r.result) return "failed";
  if (!r.preview) return "loading";
  if (r.preview.blocked) return "blocked";
  if (r.preview.behind > 0) {
    if (r.preview.rebase_blocked) return "blocked";
    return r.rebase ? "ready" : "need";
  }
  return "ready";
}

/** Goes in "Push N repos": not pushed yet, nothing blocking it, opted in to the rebase when behind. A failed one may be tried again. */
function canPush(r: RepoPush | undefined): boolean {
  const p = r?.preview;
  if (!r || r.pushed || !p || p.blocked) return false;
  return p.behind === 0 || (r.rebase && !p.rebase_blocked);
}

const DOT: Record<PushStatus, string> = {
  loading: shell.dot_skip,
  ready: shell.dot_ready,
  need: shell.dot_need,
  blocked: shell.dot_skip,
  pushed: shell.dot_committed,
  failed: shell.dot_failed,
};

/**
 * The session's own repo pushes its base once nothing is left to land
 * (isolated), else what the opener asked; any other repo works in place and
 * pushes its branch.
 */
function targetFor(repo: SessionRepo, asked: PushTarget): PushTarget {
  if (!repo.primary && !repo.isolated) return "branch";
  if (repo.isolated && (repo.unlanded_count ?? 0) === 0) return "base";
  return asked;
}

/** Several repos (layout B, as CommitDialog's): a list on the left, the selected repo's push on the right. */
function MultiPush({
  sessionId,
  routerClient,
  target,
  onClose,
  onPushed,
  onAskAgent,
  repos,
}: PushDialogProps & { repos: SessionRepo[] }) {
  const titleId = useId();
  const [pushes, setPushes] = useState<Record<string, RepoPush>>(() =>
    Object.fromEntries(
      repos.map((r) => [
        r.root,
        { target: targetFor(r, target), rebase: false, pushing: false },
      ]),
    ),
  );
  const [pane, setPane] = useState(
    (repos.find((r) => r.primary) ?? repos[0]).root,
  );
  const [running, setRunning] = useState(false);

  const patch = (root: string, change: Partial<RepoPush>) =>
    setPushes((cur) =>
      cur[root] ? { ...cur, [root]: { ...cur[root], ...change } } : cur,
    );

  // A preview fetches, so each repo's arrives on its own; a later fetch of
  // the same repo wins over an earlier one still on its way.
  const loads = useRef(new Map<string, number>());
  function load(root: string, repoTarget: PushTarget, notice?: string) {
    const n = (loads.current.get(root) ?? 0) + 1;
    loads.current.set(root, n);
    patch(root, { preview: undefined, loadError: undefined, notice });
    routerClient
      .getPushPreview(sessionId, { repo: root, target: repoTarget })
      .then((p) => {
        if (loads.current.get(root) === n) patch(root, { preview: p });
      })
      .catch((err: unknown) => {
        if (loads.current.get(root) === n)
          patch(root, {
            loadError: err instanceof Error ? err.message : String(err),
          });
      });
  }

  useEffect(() => {
    // Every listed repo at once, when the dialog opens.
    for (const r of repos) load(r.root, targetFor(r, target));
    // Mounted per opening; the list is the one it opened with.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const ready = repos.filter((r) => canPush(pushes[r.root]));

  async function pushAll() {
    setRunning(true);
    let failed: string | undefined;
    for (const repo of ready) {
      const root = repo.root;
      const r = pushes[root];
      const preview = r.preview!;
      patch(root, { pushing: true, result: undefined, notice: undefined });
      try {
        const res = await routerClient.pushSession(sessionId, {
          repo: root,
          target: r.target,
          rebase: (preview.behind > 0 && r.rebase) || undefined,
          expect: preview.head,
        });
        if (res.outcome === "pushed") {
          patch(root, { pushed: res });
          onPushed(res);
        } else if (
          res.outcome === "refused" &&
          (res.reason === "moved" || res.reason === "behind")
        ) {
          // Stale: this repo's preview again, saying why.
          failed ??= root;
          load(root, r.target, pushRefusalCopy(res.reason));
        } else {
          failed ??= root;
          patch(root, { result: res });
        }
      } catch (err) {
        failed ??= root;
        patch(root, {
          result: {
            outcome: "refused",
            detail: err instanceof Error ? err.message : String(err),
          },
        });
      } finally {
        patch(root, { pushing: false });
      }
    }
    setRunning(false);
    if (!failed) {
      onClose();
      return;
    }
    setPane(failed);
  }

  const current = repos.find((r) => r.root === pane)!;
  const cur = pushes[pane];
  const anyPushed = repos.some((r) => pushes[r.root]?.pushed);

  return (
    <div className={shell.backdrop} onMouseDown={onClose}>
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={cx(shell.dialog, shell.wide)}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className={shell.head}>
          <h2 id={titleId} className={shell.title}>
            Push this session's repos?
          </h2>
          <p className={shell.subtitle}>
            One push per repo, one at a time · never forced
          </p>
        </div>
        <div className={cx(shell.main, shell.mainSplit)}>
          <div className={shell.split}>
            <div className={shell.rlist} role="group" aria-label="Repos">
              {repos.map((r) => (
                <PushItem
                  key={r.root}
                  repo={r}
                  push={pushes[r.root]}
                  on={r.root === pane}
                  onSelect={() => setPane(r.root)}
                />
              ))}
            </div>
            <div className={shell.rpane} data-repo={pane}>
              <RepoPushPane
                repo={current}
                push={cur}
                disabled={running}
                onRebase={(rebase) => patch(pane, { rebase })}
                onAskAgent={
                  onAskAgent &&
                  ((text) => {
                    onAskAgent(text);
                    onClose();
                  })
                }
              />
            </div>
          </div>
        </div>
        <div className={shell.foot}>
          <span className={shell.spacer} />
          <Button variant="ghost" onClick={onClose}>
            {anyPushed ? "Done" : "Cancel"}
          </Button>
          <Button
            variant="primary"
            disabled={running || ready.length === 0}
            onClick={() => void pushAll()}
          >
            {running ? "Pushing…" : `Push ${plural(ready.length, "repo")}`}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** One repo in the list: name, ↑ahead · ↓behind · branch, and its dot. */
function PushItem({
  repo,
  push,
  on,
  onSelect,
}: {
  repo: SessionRepo;
  push: RepoPush | undefined;
  on: boolean;
  onSelect: () => void;
}) {
  const status = pushStatus(push);
  const p = push?.preview;
  let sub: ReactNode;
  if (status === "pushed") sub = <span className={shell.tagOk}>✓ pushed</span>;
  else if (status === "loading") sub = "Fetching…";
  else if (status === "blocked") sub = "can't push";
  else if (status === "failed") sub = "didn't push · see why";
  else
    sub = [
      `↑${p?.ahead ?? repo.unpushed_count ?? 0}`,
      p && p.behind > 0 && `↓${p.behind}`,
      p?.branch,
    ]
      .filter(Boolean)
      .join(" · ");
  return (
    <button
      type="button"
      className={cx(shell.ritem, on && shell.ritemOn)}
      aria-current={on || undefined}
      title={repo.root}
      data-status={status}
      onClick={onSelect}
    >
      <span className={shell.rtitle}>
        <span className={cx(shell.dot, DOT[status])} aria-hidden="true" />
        {repoName(repo.root)}
      </span>
      <span className={shell.rsub}>{sub}</span>
    </button>
  );
}

/** The selected repo: what its push would send, its rebase opt-in, how it went. */
function RepoPushPane({
  repo,
  push,
  disabled,
  onRebase,
  onAskAgent,
}: {
  repo: SessionRepo;
  push: RepoPush | undefined;
  disabled: boolean;
  onRebase: (rebase: boolean) => void;
  onAskAgent?: (text: string) => void;
}) {
  const p = push?.preview;
  return (
    <div className={shell.pane}>
      <div className={shell.paneHead}>
        <span className={shell.paneName}>
          {p ? pushTitle(p) : repoName(repo.root)}
        </span>
        <span className={shell.where} title={repo.root}>
          {displayPath(repo.root)}
        </span>
      </div>
      {p?.new_branch && <div className={shell.muted}>{newBranchNote(p)}</div>}
      {push?.pushed ? (
        <div className={shell.partial}>{pushedCopy(push.pushed)}.</div>
      ) : push?.loadError ? (
        <div className={shell.error}>
          Couldn't check the remote: {push.loadError}
        </div>
      ) : !p ? (
        <Fetching remote={undefined} />
      ) : (
        <PreviewBody
          preview={p}
          notice={push?.notice}
          result={push?.result}
          onAskAgent={onAskAgent}
          rebase={{
            checked: Boolean(push?.rebase),
            disabled: disabled || Boolean(push?.pushing),
            onChange: onRebase,
          }}
        />
      )}
    </div>
  );
}

function pushTitle(p: PushPreview): string {
  return `Push ${p.branch ?? "this branch"}${p.remote ? ` to ${p.remote}` : ""}${p.new_branch ? " as a new branch" : ""}?`;
}

function newBranchNote(p: PushPreview): string {
  return `${p.remote} has no ${p.branch} yet: the push creates it and tracks it.`;
}

function Fetching({ remote }: { remote: string | undefined }) {
  return (
    <div className={styles.loading} role="status">
      <span className={styles.spinner} aria-hidden="true" />
      Fetching {remote ?? "the remote"}…
    </div>
  );
}

/**
 * What a push would send, the line about the fetch, why it can't run, what's
 * coming in (and, in the multi-repo dialog, the rebase opt-in), and how the
 * last try went.
 */
function PreviewBody({
  preview,
  notice,
  result,
  onAskAgent,
  rebase,
}: {
  preview: PushPreview;
  notice?: string;
  result?: PushResult;
  onAskAgent?: (text: string) => void;
  /** The multi-repo dialog's per-repo opt-in; the single pane's main button is its own. */
  rebase?: { checked: boolean; disabled: boolean; onChange: (on: boolean) => void };
}) {
  const remote = preview.remote;
  const upstream = remoteRef(remote, preview.remote_branch);
  const behind = preview.behind > 0;
  const blocked = preview.blocked;
  const rebaseBlocked = behind ? preview.rebase_blocked : undefined;
  return (
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
            {upstream} has {plural(preview.behind, "commit")} you don't have
          </div>
          <CommitList
            commits={preview.incoming}
            total={preview.behind}
            label="Incoming commits"
          />
          {rebase && (
            <label className={styles.optIn}>
              <input
                type="checkbox"
                checked={rebase.checked && !rebaseBlocked}
                disabled={rebase.disabled || Boolean(rebaseBlocked)}
                onChange={(e) => rebase.onChange(e.target.checked)}
              />
              Rebase onto {upstream} first
            </label>
          )}
          {rebaseBlocked && (
            <div className={styles.blocked} data-reason={rebaseBlocked.reason}>
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
          {rebase && !rebaseBlocked && !rebase.checked && (
            <div className={shell.muted}>
              Left out of this push until you tick it.
            </div>
          )}
        </div>
      )}
      {result && (
        <Outcome
          result={result}
          branch={preview.branch}
          upstream={upstream}
          onAskAgent={onAskAgent}
        />
      )}
    </>
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
