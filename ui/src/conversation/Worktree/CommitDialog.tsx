import {
  useEffect,
  useId,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { Button } from "../../components/Button/Button";
import {
  CommitRefusedError,
  MoveRefusedError,
  type RouterClient,
} from "../../router/client";
import type {
  CommitError,
  CommitPath,
  CommitPlan,
  CommitPlanFile,
  CommitPlanOutside,
  CommitPlanRepo,
  CommitResult,
  MoveFolder,
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

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

function dirName(path: string): string {
  return path.slice(0, path.lastIndexOf("/")) || "/";
}

/** ".html" for "plan.html"; none for "Makefile" or ".env". */
function extOf(path: string): string | undefined {
  const name = baseName(path);
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot).toLowerCase() : undefined;
}

/** How many suggestions the folder picker shows at once. */
const SUGGESTIONS = 6;

/**
 * A file on its way into a repo (D4): the folder picker at the top of that
 * repo's pane. Nothing moves until "Move here".
 */
interface MoveState {
  file: CommitPlanOutside;
  /** The repo root it goes into. */
  repo: string;
  /** The folder field: relative to the repo, empty for its top. */
  dir: string;
  /** The user typed in the field: a late folder list mustn't overwrite it. */
  touched: boolean;
  /** The repo's folders, once fetched. */
  folders?: MoveFolder[];
  /** The highlighted suggestion. */
  hi: number;
  busy: boolean;
  /** The Router's refusal, in its own words. */
  error?: string;
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
  const [move, setMove] = useState<MoveState | null>(null);
  // Files moved in while the dialog is open, by repo: they carry a "moved" tag.
  const [moved, setMoved] = useState<Record<string, string[]>>({});
  // The outside file being dragged, and the repo item it's over.
  const [dragging, setDragging] = useState<CommitPlanOutside | null>(null);
  const [dropOver, setDropOver] = useState<string | null>(null);
  // Each repo's folders, fetched once per repo and extension while open.
  const folderCache = useRef(new Map<string, MoveFolder[]>());

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
    setMove(null);
    setMoved({});
    setDragging(null);
    setDropOver(null);
    folderCache.current = new Map();
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

  // Opens the folder picker for a file headed into `root`: from a drop on a
  // repo in the list, or from Move to…. Its folders load once per repo and
  // extension; the first is filled in when it already holds files like this.
  function startMove(file: CommitPlanOutside, root: string) {
    setDragging(null);
    setDropOver(null);
    setPane(root);
    setMove({ file, repo: root, dir: "", touched: false, hi: 0, busy: false });
    const ext = extOf(file.path);
    const key = `${root}\n${ext ?? ""}`;
    const apply = (folders: MoveFolder[]) =>
      setMove((m) =>
        m && m.file.path === file.path && m.repo === root
          ? {
              ...m,
              folders,
              dir:
                m.touched || !folders[0]?.matches ? m.dir : folders[0].path,
            }
          : m,
      );
    const cached = folderCache.current.get(key);
    if (cached) {
      apply(cached);
      return;
    }
    routerClient
      .getMoveFolders(sessionId, root, ext)
      .then((folders) => {
        folderCache.current.set(key, folders);
        apply(folders);
      })
      .catch(() => apply([]));
  }

  function cancelMove() {
    // A repo that isn't in the plan had a pane only for its picker.
    if (move && !plan?.repos.some((r) => r.root === move.repo))
      setPane(OUTSIDE);
    setMove(null);
  }

  async function confirmMove() {
    if (!move || move.busy) return;
    const { file, repo: root } = move;
    setMove({ ...move, busy: true, error: undefined });
    try {
      const res = await routerClient.moveFile(sessionId, {
        path: file.path,
        repo: root,
        dir: move.dir.trim() || undefined,
      });
      const rel = res.path.startsWith(`${root}/`)
        ? res.path.slice(root.length + 1)
        : baseName(res.path);
      setMoved((cur) => ({ ...cur, [root]: [...(cur[root] ?? []), rel] }));
      setMove(null);
      reloadAfterMove(root, rel);
    } catch (err) {
      setMove((m) =>
        m && {
          ...m,
          busy: false,
          error:
            err instanceof MoveRefusedError
              ? err.body.error
              : err instanceof Error
                ? err.message
                : String(err),
        },
      );
    }
  }

  // The plan again after a move: the target repo's files (joining the list
  // if it had none) with the moved file checked, the outside files and move
  // targets as they are now. Every other repo keeps its files, checks and
  // message, as reloadRepo does.
  function reloadAfterMove(root: string, rel: string) {
    routerClient
      .getCommitPlan(sessionId)
      .then((p) => {
        const fresh = p.repos.find((r) => r.root === root);
        setPlan((cur) => {
          if (!cur) return p;
          const had = cur.repos.some((r) => r.root === root);
          const repos = cur.repos.map((r) =>
            r.root === root ? { ...r, files: fresh?.files ?? [] } : r,
          );
          if (fresh && !had) repos.push(fresh);
          return { ...p, repos };
        });
        setDrafts((cur) => {
          const d = cur[root];
          if (d) {
            const checked = new Set(d.checked).add(rel);
            return { ...cur, [root]: { ...d, checked } };
          }
          return fresh ? { ...cur, [root]: freshDraft(fresh) } : cur;
        });
        setPane(root);
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
    moved: moved[repo.root],
  });

  const moveTargets = plan?.move_targets ?? [];
  const busy = committing || Boolean(move?.busy);
  const picker = move && (
    <MovePicker
      move={move}
      onChange={(dir) =>
        setMove((m) => m && { ...m, dir, touched: true, hi: 0, error: undefined })
      }
      onHighlight={(hi) => setMove((m) => m && { ...m, hi })}
      onCancel={cancelMove}
      onConfirm={() => void confirmMove()}
    />
  );
  const outsideProps = {
    files: outside,
    targets: moveTargets,
    disabled: busy,
    onMoveTo: startMove,
  };

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
    body = (
      <>
        {picker}
        <OutsideList {...outsideProps} />
        {moveTargets.length > 0 && (
          <div className={styles.muted}>Use Move to… to put one in a repo.</div>
        )}
      </>
    );
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
          {repos.map((r) => {
            const file =
              dragging && moveTargets.includes(r.root) ? dragging : null;
            return (
              <RepoItem
                key={r.root}
                repo={r}
                draft={drafts[r.root]}
                on={r.root === pane}
                onSelect={() => setPane(r.root)}
                drop={
                  file
                    ? {
                        over: dropOver === r.root,
                        onOver: () => setDropOver(r.root),
                        onLeave: () =>
                          setDropOver((o) => (o === r.root ? null : o)),
                        onDrop: () => startMove(file, r.root),
                      }
                    : undefined
                }
              />
            );
          })}
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
            <>
              {move?.repo === current.root && picker}
              <RepoPane key={current.root} {...paneProps(current)} />
            </>
          ) : move && move.repo === pane ? (
            // A repo with nothing uncommitted yet: just the picker.
            <>
              <div className={styles.paneHead}>
                <span className={styles.paneName}>{repoName(move.repo)}</span>
                <span className={styles.where} title={move.repo}>
                  {displayPath(move.repo)}
                </span>
              </div>
              {picker}
            </>
          ) : (
            <>
              <div className={styles.paneHead}>
                <span className={styles.paneName}>Not in a repo</span>
              </div>
              <OutsideList
                {...outsideProps}
                onDragStart={setDragging}
                onDragEnd={() => {
                  setDragging(null);
                  setDropOver(null);
                }}
              />
              <div className={styles.muted}>
                These can't be committed where they are.
                {moveTargets.length > 0 &&
                  " Drag one onto a repo on the left, or use Move to…"}
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
  drop,
}: {
  repo: CommitPlanRepo;
  draft: RepoDraft | undefined;
  on: boolean;
  onSelect: () => void;
  /** Set while a file is dragged that can move into this repo. */
  drop?: {
    over: boolean;
    onOver: () => void;
    onLeave: () => void;
    onDrop: () => void;
  };
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
      className={cx(
        styles.ritem,
        on && styles.ritemOn,
        drop && styles.ritemDroppable,
        drop?.over && styles.ritemDrop,
      )}
      aria-current={on || undefined}
      title={repo.root}
      data-status={status}
      data-drop={drop ? (drop.over ? "over" : "target") : undefined}
      onClick={onSelect}
      onDragOver={
        drop &&
        ((e: DragEvent) => {
          e.preventDefault();
          if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
          if (!drop.over) drop.onOver();
        })
      }
      onDragLeave={drop?.onLeave}
      onDrop={
        drop &&
        ((e: DragEvent) => {
          e.preventDefault();
          drop.onDrop();
        })
      }
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
  moved,
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
  /** Files moved in from outside every repo while the dialog is open. */
  moved?: string[];
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
              {moved?.includes(f.path) && (
                <span className={styles.tagOk}>moved</span>
              )}
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

/**
 * The files the session changed outside every repo: never checkable, but
 * each can move into a repo (D4) — dragged onto one in the repo list, or
 * through its Move to… menu, the way without a mouse.
 */
function OutsideList({
  files,
  targets,
  disabled,
  onMoveTo,
  onDragStart,
  onDragEnd,
}: {
  files: CommitPlanOutside[];
  /** CommitPlan.move_targets: none, and the rows only list. */
  targets: string[];
  disabled: boolean;
  onMoveTo: (file: CommitPlanOutside, root: string) => void;
  /** Set where there's a repo list to drop on. */
  onDragStart?: (file: CommitPlanOutside) => void;
  onDragEnd?: () => void;
}) {
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const movable = targets.length > 0 && !disabled;
  const draggable = movable && Boolean(onDragStart);
  return (
    <div className={styles.files} role="group" aria-label="Not in a repo">
      {files.map((f) => (
        <div
          key={f.path}
          className={cx(styles.file, styles.outsideFile)}
          draggable={draggable || undefined}
          data-path={f.path}
          onDragStart={
            draggable
              ? (e: DragEvent) => {
                  e.dataTransfer?.setData("text/plain", f.path);
                  if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
                  onDragStart?.(f);
                }
              : undefined
          }
          onDragEnd={draggable ? onDragEnd : undefined}
        >
          {draggable && (
            <span
              className={styles.grip}
              title="Drag onto a repo on the left"
              aria-hidden="true"
            >
              ⋮⋮
            </span>
          )}
          <KindMark kind={f.kind} />
          <span className={styles.path} title={f.path}>
            {displayPath(f.path)}
          </span>
          {targets.length > 0 && (
            <MoveToMenu
              file={f}
              targets={targets}
              open={menuFor === f.path}
              disabled={disabled}
              onOpen={(o) => setMenuFor(o ? f.path : null)}
              onPick={(root) => {
                setMenuFor(null);
                onMoveTo(f, root);
              }}
            />
          )}
        </div>
      ))}
    </div>
  );
}

/** "Move to…" and its repo menu: arrow keys move, Enter picks, Escape closes. */
function MoveToMenu({
  file,
  targets,
  open,
  disabled,
  onOpen,
  onPick,
}: {
  file: CommitPlanOutside;
  targets: string[];
  open: boolean;
  disabled: boolean;
  onOpen: (open: boolean) => void;
  onPick: (root: string) => void;
}) {
  const menuId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
  }, [open]);

  function close() {
    onOpen(false);
    triggerRef.current?.focus();
  }

  function onMenuKey(e: ReactKeyboardEvent) {
    const items = [
      ...(menuRef.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ??
        []),
    ];
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const d = e.key === "ArrowDown" ? 1 : -1;
      items[(i + d + items.length) % items.length]?.focus();
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      items[e.key === "Home" ? 0 : items.length - 1]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      // The dialog closes on Escape; here it only closes the menu.
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
      }
      close();
    }
  }

  return (
    <span className={styles.moveTo}>
      <button
        ref={triggerRef}
        type="button"
        className={styles.linkbtn}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={`Move ${baseName(file.path)} to…`}
        disabled={disabled}
        onClick={() => onOpen(!open)}
      >
        Move to…
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={`Move ${baseName(file.path)} into`}
          className={styles.menu}
          onKeyDown={onMenuKey}
          onBlur={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null))
              onOpen(false);
          }}
        >
          {targets.map((root) => (
            <button
              key={root}
              type="button"
              role="menuitem"
              className={styles.menuItem}
              onClick={() => onPick(root)}
            >
              {repoName(root)}
              <span className={styles.menuWhere}>{displayPath(root)}</span>
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

/**
 * The folder picker at the top of a repo's pane (D4, mockup §1 "after the
 * drop"): the repo's path, then a field for the folder inside it, with the
 * repo's folders as suggestions — those already holding files like this one
 * first. Moving happens on "Move here"; a refusal stays here, in the
 * Router's words.
 */
function MovePicker({
  move,
  onChange,
  onHighlight,
  onCancel,
  onConfirm,
}: {
  move: MoveState;
  onChange: (dir: string) => void;
  onHighlight: (hi: number) => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const name = repoName(move.repo);
  const ext = extOf(move.file.path);
  const suggestions = (move.folders ?? [])
    .filter((f) => f.path.startsWith(move.dir))
    .slice(0, SUGGESTIONS);
  const hi = Math.min(move.hi, suggestions.length - 1);

  useEffect(() => {
    inputRef.current?.focus();
  }, [move.file.path, move.repo]);

  function onKey(e: ReactKeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (suggestions.length === 0) return;
      const d = e.key === "ArrowDown" ? 1 : -1;
      onHighlight((hi + d + suggestions.length) % suggestions.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      // Enter takes the highlighted folder; once the field holds it, Enter moves.
      const pick = hi >= 0 ? suggestions[hi] : undefined;
      if (pick && pick.path !== move.dir) onChange(pick.path);
      else onConfirm();
    } else if (e.key === "Escape") {
      // The dialog closes on Escape; here it only closes the picker.
      e.preventDefault();
      e.stopPropagation();
      onCancel();
    }
  }

  return (
    <div
      className={cx(styles.moving, move.error && styles.movingBad)}
      role="group"
      aria-label={`Move ${baseName(move.file.path)} into ${name}`}
      data-testid="move-picker"
    >
      <div className={styles.moveRow}>
        <KindMark kind={move.file.kind} />
        <span className={styles.mono}>{baseName(move.file.path)}</span>
        <span className={styles.muted}>→ {name}</span>
      </div>
      <div className={styles.pathfield}>
        <span className={styles.pathRoot}>{displayPath(move.repo)}/</span>
        <input
          ref={inputRef}
          value={move.dir}
          placeholder="the repo's top"
          aria-label={`Folder in ${name}`}
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={suggestions.length > 0}
          aria-controls={listId}
          aria-activedescendant={
            hi >= 0 && suggestions[hi] ? `${listId}-${hi}` : undefined
          }
          disabled={move.busy}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKey}
        />
      </div>
      {move.folders && suggestions.length > 0 && (
        <ul id={listId} role="listbox" className={styles.suggest}>
          {suggestions.map((f, i) => (
            <li
              key={f.path}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === hi}
              className={cx(i === hi && styles.suggestHi)}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => onHighlight(i)}
              onClick={() => {
                onChange(f.path);
                inputRef.current?.focus();
              }}
            >
              {f.path}
              {f.matches && ext ? (
                <span className={styles.suggestNote}>
                  {plural(f.matches, `${ext} file`)}
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {move.error && (
        <div className={styles.moveError} role="alert">
          {move.error}
        </div>
      )}
      <div className={styles.muted}>
        Moves it out of {displayPath(dirName(move.file.path))}. The agent is
        told the new path.
      </div>
      <div className={styles.muted}>
        This happens now, not on Commit — Cancel won't put it back.
      </div>
      <div className={styles.moveRow}>
        <span className={styles.spacer} />
        <Button variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          variant="primary"
          size="sm"
          disabled={move.busy}
          onClick={onConfirm}
        >
          {move.busy ? "Moving…" : "Move here"}
        </Button>
      </div>
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
