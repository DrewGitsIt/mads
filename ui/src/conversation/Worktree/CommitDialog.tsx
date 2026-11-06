import { useEffect, useId, useState } from "react";
import { Button } from "../../components/Button/Button";
import { CommitRefusedError, type RouterClient } from "../../router/client";
import type {
  CommitError,
  CommitPath,
  CommitPlan,
  CommitPlanFile,
  CommitPlanOutside,
  CommitPlanRepo,
  CommitResult,
} from "../../router/types";
import { displayPath } from "../../files/displayPath";
import {
  commitPathReason,
  notMineNote,
  plural,
  repoName,
  shortHash,
} from "./worktreeCopy";
import styles from "./CommitDialog.module.css";

export interface CommitDialogProps {
  open: boolean;
  sessionId: string;
  routerClient: RouterClient;
  /** A Fast Model can draft — shows "Draft with Fast Model". */
  fastModel: boolean;
  /** A refusal to show on open — the one-click Commit falling back to the dialog. It targeted the primary (or only) repo. */
  initialError?: CommitError;
  onClose: () => void;
  /** Once per commit made — one per repo in a multi-repo commit. */
  onCommitted: (result: CommitResult) => void;
  /** Opens a file's diff in the Changes tab (absolute path, as the change list records it). */
  onOpenFile?: (path: string) => void;
}

const KIND_CLASS: Record<string, string> = {
  added: styles.kindAdded,
  modified: styles.kindModified,
  deleted: styles.kindDeleted,
};

/** The "Not in a repo" pane's key: never a repo root, which is absolute. */
const OUTSIDE = "outside";

function cx(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

function joinPath(root: string, path: string): string {
  return `${root.replace(/\/+$/, "")}/${path}`;
}

/** One repo's half of the dialog: what's checked, its message, and what its last Commit said. */
interface RepoDraft {
  checked: Set<string>;
  message: string;
  drafting: boolean;
  draftNote?: string;
  error?: CommitError;
  /** Per-path outcomes the server named: a refusal's, or what a partial commit skipped. */
  notes: CommitPath[];
  /** The commit this dialog made here. */
  result?: CommitResult;
  /** Committed with nothing skipped: the repo is finished. */
  done: boolean;
}

/** The repo list's dot: ready (green), needs a message (amber), nothing selected (grey), failed (red). */
type RepoStatus = "ready" | "need" | "skip" | "failed" | "committed";

/** The session's own files start checked; someone else's start unchecked (D2). A refusal's selection overrides both. */
function freshDraft(repo: CommitPlanRepo, initial?: CommitError): RepoDraft {
  const keep = initial?.selection?.length ? initial.selection : undefined;
  const checked = repo.files
    .filter((f) => (keep ? keep.includes(f.path) : f.mine))
    .map((f) => f.path);
  return {
    checked: new Set(checked),
    message: "",
    drafting: false,
    error: initial,
    notes: initial?.paths ?? [],
    done: false,
  };
}

function selectedPaths(
  repo: CommitPlanRepo,
  d: RepoDraft | undefined,
): string[] {
  return d
    ? repo.files.filter((f) => d.checked.has(f.path)).map((f) => f.path)
    : [];
}

function statusOf(repo: CommitPlanRepo, d: RepoDraft | undefined): RepoStatus {
  if (!d) return "skip";
  if (d.error) return "failed";
  if (d.done) return "committed";
  if (selectedPaths(repo, d).length === 0)
    return d.result ? "committed" : "skip";
  return d.message.trim() ? "ready" : "need";
}

/** What the one-click Commit acted on: the folder's repo, else the only one. */
function targetOf(plan: CommitPlan): CommitPlanRepo | undefined {
  return plan.repos.find((r) => r.primary) ?? plan.repos[0];
}

/**
 * Commit this session's changes (docs/COMMIT-LAND-PUSH-DESIGN.md D2,
 * mockup commit-land-push.html §1–2). Lists everything uncommitted in the
 * session's repos — its own files checked, everyone else's not — fetched
 * when the dialog opens, never per render. Several repos (or files outside
 * every repo) get a repo list on the left and one message per repo; one repo
 * gets a single pane. Repos commit one at a time; a refusal renders in that
 * repo's pane and keeps its selection, so a failing hook can be fixed and
 * the same commit retried, and the others still commit.
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
  const titleId = useId();
  const [plan, setPlan] = useState<CommitPlan | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [drafts, setDrafts] = useState<Record<string, RepoDraft>>({});
  const [pane, setPane] = useState<string>("");
  const [committing, setCommitting] = useState(false);
  // initialError with no repo to show it in (nothing left to commit).
  const [strayError, setStrayError] = useState<CommitError>();

  const patch = (
    root: string,
    change: Partial<RepoDraft> | ((d: RepoDraft) => Partial<RepoDraft>),
  ) =>
    setDrafts((cur) => {
      const d = cur[root];
      if (!d) return cur;
      return {
        ...cur,
        [root]: {
          ...d,
          ...(typeof change === "function" ? change(d) : change),
        },
      };
    });

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setPlan(null);
    setLoadError(undefined);
    setDrafts({});
    setStrayError(undefined);
    setCommitting(false);
    routerClient
      .getCommitPlan(sessionId)
      .then((p) => {
        if (cancelled) return;
        const target = initialError ? targetOf(p) : undefined;
        setPlan(p);
        setDrafts(
          Object.fromEntries(
            p.repos.map((r) => [
              r.root,
              freshDraft(r, r === target ? initialError : undefined),
            ]),
          ),
        );
        setPane(
          target?.root ?? p.repos[0]?.root ?? (p.outside.length ? OUTSIDE : ""),
        );
        if (initialError && !target) setStrayError(initialError);
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setLoadError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [open, sessionId, routerClient, initialError]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  // A repo's files again after a commit that left some (or found none):
  // the others keep what the user did with them.
  function reloadRepo(root: string) {
    routerClient
      .getCommitPlan(sessionId)
      .then((p) => {
        const files = p.repos.find((r) => r.root === root)?.files ?? [];
        setPlan(
          (cur) =>
            cur && {
              ...cur,
              repos: cur.repos.map((r) =>
                r.root === root ? { ...r, files } : r,
              ),
            },
        );
        patch(root, {
          checked: new Set(files.filter((f) => f.mine).map((f) => f.path)),
        });
      })
      .catch(() => {});
  }

  function toggle(root: string, path: string) {
    patch(root, (d) => {
      const checked = new Set(d.checked);
      if (checked.has(path)) checked.delete(path);
      else checked.add(path);
      return { checked };
    });
  }

  function setAll(root: string, files: CommitPlanFile[], on: boolean) {
    patch(root, (d) => {
      const checked = new Set(d.checked);
      for (const f of files) {
        if (on) checked.add(f.path);
        else checked.delete(f.path);
      }
      return { checked };
    });
  }

  async function draft(repo: CommitPlanRepo) {
    const paths = selectedPaths(repo, drafts[repo.root]);
    patch(repo.root, { drafting: true, draftNote: undefined });
    try {
      const d = await routerClient.draftCommitMessage(
        sessionId,
        paths,
        repo.root,
      );
      patch(repo.root, {
        message: d.message,
        draftNote: d.truncated
          ? "The diff was too long to send whole, so this draft saw only part of it."
          : undefined,
      });
    } catch (err) {
      patch(repo.root, {
        draftNote:
          err instanceof CommitRefusedError
            ? err.body.error
            : "The Fast Model couldn't draft a message.",
      });
    } finally {
      patch(repo.root, { drafting: false });
    }
  }

  const repos = plan?.repos ?? [];
  const outside = plan?.outside ?? [];
  // Repos the button commits: something checked, not finished yet.
  const targets = repos.filter(
    (r) => !drafts[r.root]?.done && selectedPaths(r, drafts[r.root]).length > 0,
  );
  const unwritten = targets.filter((r) => !drafts[r.root]?.message.trim());

  // "Draft all messages" fills the ones still empty; it never overwrites a message.
  async function draftAll() {
    for (const r of unwritten) await draft(r);
  }

  async function commit() {
    setCommitting(true);
    let failed: string | undefined;
    let skippedAny = false;
    for (const repo of targets) {
      const d = drafts[repo.root];
      const root = repo.root;
      patch(root, { error: undefined, notes: [] });
      try {
        const res = await routerClient.commitSession(sessionId, {
          repo: root,
          paths: selectedPaths(repo, d),
          message: d.message.trim(),
        });
        onCommitted(res);
        const skipped = res.paths.filter((p) => p.status !== "committed");
        if (skipped.length === 0) {
          patch(root, { result: res, done: true });
        } else {
          // Committed, but not everything asked for: say which and why.
          skippedAny = true;
          patch(root, { result: res, notes: skipped, message: "" });
          reloadRepo(root);
        }
      } catch (err) {
        failed ??= root;
        if (err instanceof CommitRefusedError) {
          patch(root, { error: err.body, notes: err.body.paths ?? [] });
          if (err.body.code === "nothing_to_commit") reloadRepo(root);
        } else {
          patch(root, {
            error: {
              code: "unexpected",
              error: err instanceof Error ? err.message : String(err),
            },
          });
        }
      }
    }
    setCommitting(false);
    if (!failed && !skippedAny) {
      onClose();
      return;
    }
    if (failed) setPane(failed);
  }

  if (!open) return null;

  const split = repos.length > 1 || (repos.length > 0 && outside.length > 0);
  const single = !split && repos.length === 1 ? repos[0] : undefined;
  const outsideOnly = plan !== null && repos.length === 0 && outside.length > 0;
  const nothing = plan !== null && repos.length === 0 && outside.length === 0;
  const anyCommitted = repos.some((r) => drafts[r.root]?.result);

  let title = "Commit this session's changes";
  let subtitle: string | undefined;
  if (outsideOnly) {
    title = "Nothing here can be committed";
    subtitle = "None of this session's changed files are in a git repo";
  } else if (split) {
    subtitle =
      "Everything uncommitted in this session's repos, whatever Changes is showing · one commit per repo, as you";
  } else if (single) {
    subtitle = [repoName(single.root), single.branch, "as you"]
      .filter(Boolean)
      .join(" · ");
  }

  const singleCount = single
    ? selectedPaths(single, drafts[single.root]).length
    : 0;
  const confirmLabel = committing
    ? "Committing…"
    : split
      ? `Commit ${plural(targets.length, "repo")}`
      : `Commit ${plural(singleCount, "file")}`;
  const confirmDisabled =
    committing || targets.length === 0 || unwritten.length > 0;

  const paneProps = (repo: CommitPlanRepo) => ({
    repo,
    draft: drafts[repo.root],
    split,
    fastModel,
    committing,
    onToggle: (path: string) => toggle(repo.root, path),
    onSetAll: (files: CommitPlanFile[], on: boolean) =>
      setAll(repo.root, files, on),
    onMessage: (message: string) => patch(repo.root, { message }),
    onDraft: () => void draft(repo),
    onOpenFile,
  });

  let body;
  if (loadError) {
    body = (
      <div className={styles.error}>
        Couldn't read this session's changes: {loadError}
      </div>
    );
  } else if (!plan) {
    body = <div className={styles.muted}>Reading this session's changes…</div>;
  } else if (outsideOnly) {
    body = <OutsideList files={outside} />;
  } else if (nothing) {
    body = (
      <div className={styles.muted}>
        Nothing left to commit: every file this session changed is committed.
      </div>
    );
  } else if (single) {
    body = <RepoPane {...paneProps(single)} />;
  } else {
    const current = repos.find((r) => r.root === pane);
    body = (
      <div className={styles.split}>
        <div className={styles.rlist} role="group" aria-label="Repos">
          {repos.map((r) => (
            <RepoItem
              key={r.root}
              repo={r}
              draft={drafts[r.root]}
              on={r.root === pane}
              onSelect={() => setPane(r.root)}
            />
          ))}
          {outside.length > 0 && (
            <button
              type="button"
              className={cx(styles.ritem, pane === OUTSIDE && styles.ritemOn)}
              aria-current={pane === OUTSIDE || undefined}
              data-status="skip"
              onClick={() => setPane(OUTSIDE)}
            >
              <span className={styles.rtitle}>
                <span
                  className={cx(styles.dot, styles.dot_skip)}
                  aria-hidden="true"
                />
                Not in a repo
              </span>
              <span className={styles.rsub}>
                {plural(outside.length, "file")} · can't commit
              </span>
            </button>
          )}
        </div>
        <div className={styles.rpane}>
          {current ? (
            <RepoPane key={current.root} {...paneProps(current)} />
          ) : (
            <>
              <div className={styles.paneHead}>
                <span className={styles.paneName}>Not in a repo</span>
              </div>
              <OutsideList files={outside} />
              <div className={styles.muted}>
                These can't be committed where they are. Moving them into a repo
                comes next.
              </div>
            </>
          )}
        </div>
      </div>
    );
  }

  const footStatus = split
    ? targets
        .map((r) => {
          const s = statusOf(r, drafts[r.root]);
          const name = repoName(r.root);
          if (s === "need") return `${name} needs a message`;
          if (s === "failed")
            return drafts[r.root]?.message.trim()
              ? `${name} failed`
              : `${name} needs a message`;
          return `${name} ready`;
        })
        .join(" · ")
    : "";
  const terminal = outsideOnly || nothing;

  return (
    <div className={styles.backdrop} onMouseDown={onClose}>
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={cx(styles.dialog, split && styles.wide)}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className={styles.head}>
          <h2 id={titleId} className={styles.title}>
            {title}
          </h2>
          {subtitle && <p className={styles.subtitle}>{subtitle}</p>}
        </div>
        <div className={cx(styles.main, split && styles.mainSplit)}>
          {body}
          {strayError && <ErrorBlock error={strayError} />}
        </div>
        <div className={styles.foot}>
          {split && fastModel && (
            <button
              type="button"
              className={styles.linkbtn}
              disabled={
                committing ||
                unwritten.length === 0 ||
                unwritten.some((r) => drafts[r.root]?.drafting)
              }
              onClick={() => void draftAll()}
            >
              Draft all messages
            </button>
          )}
          <span className={styles.spacer} />
          {footStatus && <span className={styles.muted}>{footStatus}</span>}
          {terminal ? (
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
          ) : (
            <>
              <Button variant="ghost" onClick={onClose}>
                {anyCommitted ? "Done" : "Cancel"}
              </Button>
              <Button
                variant="primary"
                disabled={confirmDisabled}
                onClick={() => void commit()}
              >
                {confirmLabel}
              </Button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** One repo in the left list: its name, how much is checked, and whether it's ready. */
function RepoItem({
  repo,
  draft,
  on,
  onSelect,
}: {
  repo: CommitPlanRepo;
  draft: RepoDraft | undefined;
  on: boolean;
  onSelect: () => void;
}) {
  const status = statusOf(repo, draft);
  const n = selectedPaths(repo, draft).length;
  let sub;
  if (status === "committed" && draft?.result) {
    sub = (
      <span className={styles.tagOk}>
        committed {shortHash(draft.result.hash)}
      </span>
    );
  } else if (status === "failed") {
    sub = "commit failed · selection kept";
  } else {
    sub = [
      `${n} of ${plural(repo.files.length, "file")}`,
      n > 0 && (draft?.message.trim() ? "message ✓" : "needs a message"),
      repo.branch,
    ]
      .filter(Boolean)
      .join(" · ");
  }
  return (
    <button
      type="button"
      className={cx(styles.ritem, on && styles.ritemOn)}
      aria-current={on || undefined}
      title={repo.root}
      data-status={status}
      onClick={onSelect}
    >
      <span className={styles.rtitle}>
        <span
          className={cx(styles.dot, styles[`dot_${status}`])}
          aria-hidden="true"
        />
        {repoName(repo.root)}
      </span>
      <span className={styles.rsub}>{sub}</span>
    </button>
  );
}

/** One repo's files, in two groups, and its message. */
function RepoPane({
  repo,
  draft,
  split,
  fastModel,
  committing,
  onToggle,
  onSetAll,
  onMessage,
  onDraft,
  onOpenFile,
}: {
  repo: CommitPlanRepo;
  draft: RepoDraft | undefined;
  split: boolean;
  fastModel: boolean;
  committing: boolean;
  onToggle: (path: string) => void;
  onSetAll: (files: CommitPlanFile[], on: boolean) => void;
  onMessage: (message: string) => void;
  onDraft: () => void;
  onOpenFile?: (path: string) => void;
}) {
  if (!draft) return null;
  const name = repoName(repo.root);
  const selected = selectedPaths(repo, draft);
  const mine = repo.files.filter((f) => f.mine);
  const others = repo.files.filter((f) => !f.mine);
  const noteFor = new Map(
    draft.notes.map((n) => [n.path, commitPathReason(n.status, n.reason)]),
  );
  const listedNotes = draft.notes.filter(
    (n) => !repo.files.some((f) => f.path === n.path),
  );
  const result = draft.result;

  const group = (label: string, files: CommitPlanFile[]) => {
    if (files.length === 0) return null;
    const allOn = files.every((f) => draft.checked.has(f.path));
    return (
      <div className={styles.group} role="group" aria-label={label}>
        <div className={styles.groupHead}>
          {label}
          <button
            type="button"
            className={styles.linkbtn}
            disabled={committing}
            onClick={() => onSetAll(files, !allOn)}
          >
            {allOn ? "uncheck all" : "check all"}
          </button>
        </div>
        {files.map((f) => {
          const on = draft.checked.has(f.path);
          const note = noteFor.get(f.path);
          return (
            <div key={f.path} className={cx(styles.file, !on && styles.off)}>
              <input
                type="checkbox"
                checked={on}
                disabled={committing}
                onChange={() => onToggle(f.path)}
                aria-label={`Include ${f.path}`}
              />
              <KindMark kind={f.kind} />
              {onOpenFile ? (
                <button
                  type="button"
                  className={styles.path}
                  title="Open its diff"
                  onClick={() => onOpenFile(joinPath(repo.root, f.path))}
                >
                  {f.path}
                </button>
              ) : (
                <span className={styles.path}>{f.path}</span>
              )}
              {note && <span className={styles.note}>{note}</span>}
              {!f.mine && <span className={styles.why}>{notMineNote(f)}</span>}
            </div>
          );
        })}
      </div>
    );
  };

  return (
    <div className={styles.pane} data-repo={repo.root}>
      {split && (
        <div className={styles.paneHead}>
          <span className={styles.paneName}>{name}</span>
          <span className={styles.where} title={repo.root}>
            {displayPath(repo.root)}
          </span>
          {repo.branch && <span className={styles.branch}>{repo.branch}</span>}
          {!draft.done && (
            <span className={styles.count}>
              {selected.length} of {plural(repo.files.length, "file")}
            </span>
          )}
        </div>
      )}
      {result && (
        <div className={styles.partial}>
          Committed{" "}
          {plural(
            result.paths.filter((p) => p.status === "committed").length,
            "file",
          )}{" "}
          as <span className={styles.hash}>{shortHash(result.hash)}</span>.
        </div>
      )}
      {draft.done ? null : (
        <>
          {repo.files.length === 0 ? (
            <div className={styles.muted}>Nothing left to commit here.</div>
          ) : (
            <div
              className={styles.files}
              role="group"
              aria-label="Files to commit"
            >
              {group("Changed by this session", mine)}
              {group("Changed by someone else", others)}
            </div>
          )}
          {listedNotes.length > 0 && (
            <ul className={styles.notes}>
              {listedNotes.map((n) => (
                <li key={n.path}>
                  <span className={styles.mono}>{n.path}</span> —{" "}
                  {commitPathReason(n.status, n.reason)}
                </li>
              ))}
            </ul>
          )}
          <textarea
            className={styles.msg}
            value={draft.message}
            placeholder="What does this change do, and why?"
            aria-label={split ? `Commit message for ${name}` : "Commit message"}
            onChange={(e) => onMessage(e.target.value)}
          />
          <div className={styles.msgbar}>
            {fastModel ? (
              <button
                type="button"
                className={styles.linkbtn}
                disabled={draft.drafting || committing || selected.length === 0}
                onClick={onDraft}
              >
                {draft.drafting ? "Drafting…" : "Draft with Fast Model"}
              </button>
            ) : (
              <span />
            )}
            {split && repo.branch && (
              <span className={styles.muted}>
                commits to <span className={styles.mono}>{repo.branch}</span>
              </span>
            )}
          </div>
          {draft.draftNote && (
            <div className={styles.muted}>{draft.draftNote}</div>
          )}
        </>
      )}
      {draft.error && <ErrorBlock error={draft.error} />}
    </div>
  );
}

/** The files the session changed outside every repo: listed, never checkable. */
function OutsideList({ files }: { files: CommitPlanOutside[] }) {
  return (
    <div className={styles.files} role="group" aria-label="Not in a repo">
      {files.map((f) => (
        <div key={f.path} className={cx(styles.file, styles.outsideFile)}>
          <KindMark kind={f.kind} />
          <span className={styles.path} title={f.path}>
            {displayPath(f.path)}
          </span>
        </div>
      ))}
    </div>
  );
}

function KindMark({ kind }: { kind: string }) {
  return (
    <span
      className={cx(styles.kind, KIND_CLASS[kind])}
      title={kind}
      aria-label={kind}
    >
      {(kind[0] ?? "?").toUpperCase()}
    </span>
  );
}

/** A refusal in words, then git's own output verbatim. */
function ErrorBlock({ error }: { error: CommitError }) {
  return (
    <div className={styles.errorBlock} role="alert">
      <div className={styles.error}>{error.error}</div>
      {error.detail && <pre className={styles.detail}>{error.detail}</pre>}
    </div>
  );
}
