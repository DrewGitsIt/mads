import { useEffect, useId, useState } from "react";
import { Button } from "../../components/Button/Button";
import { CommitRefusedError, type RouterClient } from "../../router/client";
import { useSessionRepos } from "../../router/repoStateStore";
import type { LandPreview, LandRequest, LandResult, Session } from "../../router/types";
import { CommitList, Fetching } from "./PushDialog";
import { baseSourceCopy, plural, refusalCopy, refusalDetailIsGit } from "./worktreeCopy";
import { landRepos, type LandRepo } from "./workState";
import shell from "./CommitDialog.module.css";
import styles from "./LandDialog.module.css";

export interface LandDialogProps {
  open: boolean;
  sessionId: string;
  routerClient: RouterClient;
  /** With the repo state store, says which branches have work to land (a picker, when more than one does). */
  session: Session;
  /** A Fast Model can draft the message — shows "Draft with Fast Model". */
  fastModel: boolean;
  /** Start on this worktree (a path step's own branch); else the session's first landable one. */
  initialRoot?: string;
  /**
   * Runs the Land. Default: routerClient.landSession. The conversation
   * passes useWorktreeCards' land so the dock's Landing… and the result's
   * card follow it. Undefined: a Land was already running.
   */
  land?: (req: LandRequest) => Promise<LandResult | undefined>;
  onClose: () => void;
  /** It landed; onClose follows. */
  onLanded: (result: LandResult) => void;
  /** Conflicted, or stopped for a check after mergiraf (onClose follows); the conversation's card says what's next. */
  onResult?: (result: LandResult) => void;
  /** A refusal over uncommitted files offers "Commit them…" (D6). Unset: no such button. */
  onCommitDialog?: () => void;
}

function cx(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

/**
 * Confirm a Land (docs/COMMIT-LAND-PUSH-DESIGN.md D12): squash one branch's
 * work into one commit on its base, with a message the user writes. Land
 * always asks: the base and where it came from (changeable), the commits
 * being squashed, and the message — empty, with "Draft with Fast Model",
 * never an automatic one. One branch per confirmation; with several
 * landable (the session's own and linked worktrees an agent made), a
 * picker at the top chooses which.
 */
export function LandDialog(props: LandDialogProps) {
  const { open, session, sessionId, routerClient, initialRoot, onClose } = props;
  const state = useSessionRepos(routerClient, sessionId);
  // The branches are chosen as the dialog opens, once the repo state has
  // answered (or failed: the record's own branch still lands). Nothing
  // listed — a Land again after the count went to 0 — is the session's own
  // branch, whose preview says why there's nothing to land.
  const [choices, setChoices] = useState<LandRepo[] | null>(null);
  if (open && choices === null && (state.repos !== null || state.error !== null)) {
    const listed = landRepos(session, state.repos);
    setChoices(listed.length > 0 ? listed : [{ root: session.worktree_dir ?? "", commits: 0 }]);
  }
  if (!open && choices !== null) setChoices(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open || choices === null) return null;
  const start = choices.find((c) => c.root === initialRoot) ?? choices[0];
  return <LandConfirm {...props} choices={choices} start={start} />;
}

function LandConfirm({
  sessionId,
  routerClient,
  fastModel,
  land,
  onClose,
  onLanded,
  onResult,
  onCommitDialog,
  choices,
  start,
}: LandDialogProps & { choices: LandRepo[]; start: LandRepo }) {
  const titleId = useId();
  const [picked, setPicked] = useState<LandRepo>(start);
  // A base the user chose; undefined: the inferred one.
  const [base, setBase] = useState<string>();
  const [changing, setChanging] = useState(false);
  const [preview, setPreview] = useState<LandPreview | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [message, setMessage] = useState("");
  const [drafting, setDrafting] = useState(false);
  const [draftNote, setDraftNote] = useState<string>();
  const [landing, setLanding] = useState(false);
  const [refusal, setRefusal] = useState<LandResult>();

  useEffect(() => {
    let cancelled = false;
    setPreview(null);
    setLoadError(undefined);
    routerClient
      .getLandPreview(sessionId, { repo: picked.repo, base })
      .then((p) => {
        if (!cancelled) setPreview(p);
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoadError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, picked.repo, base]);

  function pick(c: LandRepo) {
    if (c.root === picked.root) return;
    // Another branch: its own base and its own message.
    setPicked(c);
    setBase(undefined);
    setChanging(false);
    setMessage("");
    setDraftNote(undefined);
    setRefusal(undefined);
  }

  async function draft() {
    setDrafting(true);
    setDraftNote(undefined);
    try {
      const d = await routerClient.draftLandMessage(sessionId, { repo: picked.repo, base });
      setMessage(d.message);
      if (d.truncated) setDraftNote("The diff was too long to send whole, so this draft saw only part of it.");
    } catch (err) {
      setDraftNote(err instanceof CommitRefusedError ? err.body.error : "The Fast Model couldn't draft a message.");
    } finally {
      setDrafting(false);
    }
  }

  async function confirm() {
    setLanding(true);
    setRefusal(undefined);
    const req: LandRequest = { message: message.trim() };
    if (picked.repo) req.repo = picked.repo;
    if (base) req.base = base;
    try {
      const run = land ?? ((r: LandRequest) => routerClient.landSession(sessionId, r));
      const res = await run(req);
      if (!res) return;
      // The outcome first, then the close: a caller can tell a landing (or a
      // conflict) from a Cancel by the time onClose runs.
      if (res.outcome === "landed") {
        onLanded(res);
        onClose();
        return;
      }
      if (res.outcome === "conflicted" || res.outcome === "auto_resolved") {
        onResult?.(res);
        onClose();
        return;
      }
      setRefusal(res);
    } catch (err) {
      setRefusal({ outcome: "refused", detail: err instanceof Error ? err.message : String(err) });
    } finally {
      setLanding(false);
    }
  }

  const branch = preview?.branch ?? picked.branch ?? "this branch";
  const into = preview?.base ?? picked.base ?? "its base";
  const blocked = preview?.blocked;
  const source = baseSourceCopy(preview?.base_source);
  const otherBases = (preview?.bases ?? []).filter((b) => b !== preview?.base);
  const copy = refusal && refusalCopy(refusal.reason ?? "", refusal.base ?? into, refusal.files, refusal.outside);
  const showDetail =
    refusal?.detail && (!refusal.reason || refusalDetailIsGit(refusal.reason) || copy?.title === "Land didn't run");

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
            Land {branch} into {into}?
          </h2>
        </div>
        <div className={shell.main}>
          {choices.length > 1 && (
            <div className={styles.picker} role="radiogroup" aria-label="Branch to land">
              {choices.map((c) => (
                <label key={c.root} className={styles.choice} title={c.root}>
                  <input
                    type="radio"
                    name={`${titleId}-branch`}
                    checked={c.root === picked.root}
                    disabled={landing}
                    onChange={() => pick(c)}
                  />
                  {c.branch ?? c.root}
                  {c.base && <span className={styles.into}>→ {c.base}</span>}
                  <span className={styles.count}>↑{c.commits}</span>
                </label>
              ))}
            </div>
          )}
          {loadError ? (
            <div className={shell.error}>Couldn't read the branch: {loadError}</div>
          ) : !preview ? (
            <Fetching remote={undefined} text="Reading the branch…" />
          ) : (
            <>
              <div className={styles.baseRow} data-testid="land-base">
                <span className={styles.baseLabel}>Base</span>
                <span className={shell.mono}>{preview.base ?? "none found"}</span>
                {source && <span className={shell.muted}>{source}</span>}
                {otherBases.length > 0 &&
                  (changing ? (
                    <select
                      className={styles.select}
                      aria-label="Base branch"
                      value={preview.base ?? ""}
                      disabled={landing}
                      onChange={(e) => {
                        setBase(e.target.value);
                        setChanging(false);
                      }}
                    >
                      {[preview.base, ...otherBases].filter(Boolean).map((b) => (
                        <option key={b} value={b}>
                          {b}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <button
                      type="button"
                      className={shell.linkbtn}
                      disabled={landing}
                      onClick={() => setChanging(true)}
                    >
                      Change
                    </button>
                  ))}
              </div>
              {preview.commits.length > 0 && (
                <div>
                  <div className={styles.listHead}>{plural(preview.to_land, "commit")} to squash:</div>
                  <CommitList commits={preview.commits} total={preview.to_land} label="Commits to land" />
                </div>
              )}
              <div className={shell.muted}>
                Lands as one commit on {into}; {branch} then moves to it.
              </div>
              {blocked && (
                <div className={styles.blocked} data-reason={blocked.reason}>
                  {blocked.detail ?? refusalCopy(blocked.reason, into, blocked.files, blocked.outside).body}
                </div>
              )}
              <textarea
                className={shell.msg}
                value={message}
                placeholder="What does this landing bring into the base?"
                aria-label="Landing message"
                disabled={landing}
                onChange={(e) => setMessage(e.target.value)}
              />
              {fastModel && (
                <div className={shell.msgbar}>
                  <button
                    type="button"
                    className={shell.linkbtn}
                    disabled={drafting || landing}
                    onClick={() => void draft()}
                  >
                    {drafting ? "Drafting…" : "Draft with Fast Model"}
                  </button>
                  <span />
                </div>
              )}
              {draftNote && <div className={shell.muted}>{draftNote}</div>}
              {refusal && copy && (
                <div className={shell.errorBlock} role="alert" data-reason={refusal.reason}>
                  <div className={shell.error}>
                    {copy.title}. {copy.body}
                  </div>
                  {showDetail && <pre className={shell.detail}>{refusal.detail}</pre>}
                  {refusal.reason === "dirty" && onCommitDialog && (
                    <div>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => {
                          onClose();
                          onCommitDialog();
                        }}
                      >
                        Commit them…
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </>
          )}
        </div>
        <div className={shell.foot}>
          <span className={shell.spacer} />
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!preview || Boolean(blocked) || landing || message.trim() === ""}
            title={blocked?.detail}
            onClick={() => void confirm()}
          >
            {landing ? "Landing…" : `Land into ${into}`}
          </Button>
        </div>
      </div>
    </div>
  );
}
