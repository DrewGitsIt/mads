import type { Envelope, LandResult } from "../../router/types";

/**
 * Commit and Land's results as conversation cards (docs/SESSION-WORKTREES-
 * DESIGN.md D5/D6, mockup §3–§4). Built only from the event log — replayed
 * at mount, then live — so a reload shows exactly what was there before.
 * The POST answers never draw a card of their own unless their event is
 * late (see withEchoes), so an outcome renders once.
 */


interface CardBase {
  /** Stable React key and the transcript's card id. */
  id: string;
  /** The event's log position; Infinity for a not-yet-logged echo. */
  seq: number;
  timestamp?: string;
}

export type WorktreeCardModel =
  | (CardBase & {
      kind: "committed";
      hash: string;
      subject: string;
      files?: number;
      /** The Fast Model wrote the message: the one-click Commit, the only kind Undo is offered on. */
      drafted: boolean;
      undone: boolean;
      /** No later commit or land since — Undo can still apply. The server is the final word. */
      tip: boolean;
    })
  | (CardBase & { kind: "landed"; base?: string; branch?: string; mergeCommit?: string })
  | (CardBase & { kind: "conflicted"; base?: string; files: string[]; latest: boolean })
  | (CardBase & {
      kind: "refused";
      base?: string;
      reason: string;
      detail?: string;
      files?: string[];
      /** The most recent Land outcome — the only one that offers "Land again". */
      latest: boolean;
    })
  | (CardBase & { kind: "ensured"; title: string; body: string })
  /** A pipeline task merging into the session's folder (D8) committed the agent's overlapping edits first. */
  | (CardBase & { kind: "pipelineSaved"; task?: string; files: string[] })
  /** A pipeline task merged into the folder after waiting out the agent's turn: these files changed under it. */
  | (CardBase & { kind: "pipelineMerged"; task?: string; files: string[] });

export type LandCardModel = Extract<WorktreeCardModel, { kind: "landed" | "conflicted" | "refused" }>;

function isLandCard(c: WorktreeCardModel): c is LandCardModel {
  return c.kind === "landed" || c.kind === "conflicted" || c.kind === "refused";
}

type Payload = Record<string, unknown>;

const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const strs = (v: unknown): string[] | undefined =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : undefined;

/**
 * A worktree.ensured worth a card: anything that changed the folder under
 * the user, or decided not to isolate. A first, uneventful creation is the
 * normal start and stays quiet; a later "created" means the folder and its
 * branch were both gone. Null for the quiet ones.
 */
export function ensuredCopy(p: Payload, firstCreation: boolean): { title: string; body: string } | null {
  const action = str(p.action);
  const base = str(p.base) ?? "the base branch";
  const branch = str(p.branch) ?? "its branch";
  const notes: string[] = [];
  if (str(p.stale_moved_to)) {
    notes.push(`Something else was at its folder, so it was moved to ${str(p.stale_moved_to)}.`);
  }
  if (p.base_missing === true) {
    notes.push(`${base} no longer exists, so the branch started from your checkout's current commit instead.`);
  }
  switch (action) {
    case "not_isolated":
      return {
        title: "Working in your checkout directly",
        body:
          str(p.reason) === "detached_head"
            ? "Your checkout was on a detached HEAD, so this session couldn't get a branch of its own. Commit still works; there's nothing to land."
            : "This session couldn't get a folder of its own, so it works in your checkout. Commit still works; there's nothing to land.",
      };
    case "reattached":
      return {
        title: "Recreated this session's folder",
        body: [
          `Its folder was missing, so it was checked out again from ${branch}. Every commit is still there; uncommitted edits in the old folder are gone.`,
          ...notes,
        ].join(" "),
      };
    case "created":
      if (firstCreation && notes.length === 0) return null;
      return {
        title: firstCreation ? "Started this session in its own folder" : "Recreated this session's folder and branch",
        body: [
          firstCreation
            ? `It works on ${branch}, from ${base}.`
            : `Both were gone, so the session starts again from ${base}. Commits that were only on the old branch are gone.`,
          ...notes,
        ].join(" "),
      };
    case "reused":
      if (str(p.head)) {
        return {
          title: "This session's folder is off its branch",
          body: `It has ${str(p.head) === "HEAD" ? "a detached HEAD" : str(p.head)} checked out instead of ${branch}. Land waits until it's back.`,
        };
      }
      return notes.length > 0 ? { title: "Tidied this session's folder", body: notes.join(" ") } : null;
    default:
      return null;
  }
}

/** Folds the log's worktree events, in seq order, into the cards they draw. Duplicate seqs (replay ∩ live) count once. */
export function foldWorktreeEvents(envelopes: Envelope[]): WorktreeCardModel[] {
  const seen = new Set<number>();
  const sorted = [...envelopes]
    .sort((a, b) => a.seq - b.seq)
    .filter((e) => (seen.has(e.seq) ? false : (seen.add(e.seq), true)));
  const cards: WorktreeCardModel[] = [];
  let created = false;
  const notTip = () => {
    for (const c of cards) if (c.kind === "committed") c.tip = false;
  };
  for (const env of sorted) {
    const p = (env.payload ?? {}) as Payload;
    const base = { id: `wt-${env.seq}`, seq: env.seq, timestamp: env.timestamp };
    switch (env.type) {
      case "commit.created": {
        notTip();
        // Only Commit's own commits draw a card; the agent's hook-reported
        // ones and Land's merge commit (it has its own card) don't.
        if (str(p.source) !== "commit" || !str(p.hash)) break;
        cards.push({
          ...base,
          kind: "committed",
          hash: str(p.hash)!,
          subject: str(p.message) ?? "",
          files: typeof p.files === "number" ? p.files : undefined,
          drafted: p.drafted === true,
          undone: false,
          tip: true,
        });
        break;
      }
      case "commit.undone": {
        const card = cards.find((c) => c.kind === "committed" && c.hash === str(p.hash));
        if (card?.kind === "committed") {
          card.undone = true;
          card.tip = false;
        }
        break;
      }
      case "merge.completed":
      case "merge.conflicted":
      case "merge.refused": {
        // The pipeline's own land queue reuses these types for task
        // branches; only a session Land says kind "land". A task merged
        // into the session's folder (D8) draws a card only when it touched
        // the agent's work: saved its edits, or merged mid-turn.
        if (str(p.kind) !== "land") {
          if (env.type !== "merge.completed") break;
          const task = str(p.task_title);
          const saved = strs(p.auto_committed) ?? [];
          if (saved.length > 0) cards.push({ ...base, id: `${base.id}-saved`, kind: "pipelineSaved", task, files: saved });
          if (p.waited_out === true) cards.push({ ...base, kind: "pipelineMerged", task, files: strs(p.files) ?? [] });
          break;
        }
        const landBase = str(p.base) ?? str(p.target_branch);
        if (env.type === "merge.completed") {
          notTip();
          cards.push({ ...base, kind: "landed", base: landBase, branch: str(p.branch), mergeCommit: str(p.merge_commit) });
        } else if (env.type === "merge.conflicted") {
          cards.push({ ...base, kind: "conflicted", base: landBase, files: strs(p.files) ?? [], latest: false });
        } else {
          cards.push({
            ...base,
            kind: "refused",
            base: landBase,
            reason: str(p.reason) ?? "",
            detail: str(p.detail),
            files: strs(p.files),
            latest: false,
          });
        }
        break;
      }
      case "worktree.ensured": {
        const copy = ensuredCopy(p, !created);
        if (str(p.action) === "created") created = true;
        if (copy) cards.push({ ...base, kind: "ensured", ...copy });
        break;
      }
    }
  }
  markLatest(cards);
  return cards;
}

function markLatest(cards: WorktreeCardModel[]) {
  let last: LandCardModel | undefined;
  for (const c of cards) {
    if (c.kind === "conflicted" || c.kind === "refused") c.latest = false;
    if (isLandCard(c)) last = c;
  }
  if (last && (last.kind === "conflicted" || last.kind === "refused")) last.latest = true;
}

/** A Land answer as the card its event would draw — for when the event hasn't arrived. */
export function landCardFromResult(result: LandResult, at: string): LandCardModel {
  const base = { id: `land-echo-${at}`, seq: Number.POSITIVE_INFINITY, timestamp: at };
  if (result.outcome === "landed") {
    return { ...base, kind: "landed", base: result.base, branch: result.branch, mergeCommit: result.merge_commit };
  }
  if (result.outcome === "conflicted") {
    return { ...base, kind: "conflicted", base: result.base, files: result.files ?? [], latest: true };
  }
  return {
    ...base,
    kind: "refused",
    base: result.base,
    reason: result.reason ?? "",
    detail: result.detail,
    files: result.files,
    latest: true,
  };
}

/** What an action's own answer says, held until its event shows up. */
export interface WorktreeEchoes {
  /** A Land answer, and the last worktree event seq seen when it was clicked. */
  land?: { afterSeq: number; card: LandCardModel };
  /** Commit answers by hash. */
  commits: Extract<WorktreeCardModel, { kind: "committed" }>[];
}

/**
 * The event-built cards plus any answer whose event hasn't arrived — the
 * dedupe that makes an outcome render once. A Land echo drops the moment
 * any Land outcome newer than its click is logged; a commit echo the
 * moment its hash is.
 */
export function withEchoes(cards: WorktreeCardModel[], echoes: WorktreeEchoes): WorktreeCardModel[] {
  // Copies: markLatest below must not touch the caller's (memoized) cards.
  const out = cards.map((c) => ({ ...c }));
  for (const echo of echoes.commits) {
    if (!cards.some((c) => c.kind === "committed" && c.hash === echo.hash)) out.push(echo);
  }
  const land = echoes.land;
  if (land && !cards.some((c) => isLandCard(c) && c.seq > land.afterSeq)) {
    out.push(land.card);
    markLatest(out);
  }
  return out;
}
