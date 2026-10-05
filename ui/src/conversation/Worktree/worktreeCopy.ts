import type { LandRefusalReason } from "../../router/types";

/**
 * The words Commit and Land speak (docs/mockups/session-worktrees.html).
 * One home, so the conversation card, the close prompt and the tests all
 * read the same sentence for the same outcome.
 */

export function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/** "a.go", "a.go and b.go", "a.go, b.go and 3 more" — names the files without becoming a list. */
export function nameFiles(files: string[] | undefined, max = 3): string {
  const list = files ?? [];
  if (list.length === 0) return "";
  if (list.length === 1) return list[0];
  if (list.length <= max) return `${list.slice(0, -1).join(", ")} and ${list[list.length - 1]}`;
  return `${list.slice(0, max).join(", ")} and ${list.length - max} more`;
}

export function shortHash(hash: string | undefined): string {
  return (hash ?? "").slice(0, 7);
}

/** How a card is drawn: ok (green), warn (needs a decision, nothing broke), bad (your checkout said no), quiet (information). */
export type CardTone = "ok" | "warn" | "bad" | "quiet";

export interface RefusalCopy {
  title: string;
  body: string;
  tone: CardTone;
  /** The user has to fix something and come back — the card offers "Land again". */
  retry: boolean;
}

/**
 * A refused Land, in words, for every reason code the server sends
 * (pkg/contracts/session_worktree.go). `base` is the session's recorded base
 * branch; `files` are the ones the refusal names; `detail` is the server's
 * own text, shown verbatim under the sentence by the card where it helps.
 */
export function refusalCopy(
  reason: LandRefusalReason | string,
  base: string | undefined,
  files: string[] | undefined,
): RefusalCopy {
  const b = base || "the base branch";
  const named = nameFiles(files);
  switch (reason) {
    case "not_isolated":
      return {
        title: "Nothing to land",
        body: "This session works directly in your checkout, so its commits are already there.",
        tone: "quiet",
        retry: false,
      };
    case "busy":
      return {
        title: "Can't land while the agent is working",
        body: "Land waits for the current turn to finish. Nothing changed.",
        tone: "warn",
        retry: false,
      };
    case "pipeline":
      return {
        title: "Can't land while a pipeline is running",
        body: "Land waits for this session's pipeline to finish. Nothing changed.",
        tone: "warn",
        retry: false,
      };
    case "mid_merge":
      return {
        title: "This session's folder is mid-merge",
        body: "A merge or rebase was left unfinished in the session's folder. Ask the agent to finish or abort it, then Land again.",
        tone: "warn",
        retry: true,
      };
    case "off_branch":
      return {
        title: "This session's folder is off its branch",
        body: "Something else is checked out in the session's folder. Ask the agent to switch back to its branch, then Land again.",
        tone: "warn",
        retry: true,
      };
    case "dirty":
      return {
        title: "This session has uncommitted changes",
        body: named
          ? `Commit ${named} first. Land only moves committed work.`
          : "Commit them first. Land only moves committed work.",
        tone: "warn",
        retry: false,
      };
    case "nothing_to_land":
      return {
        title: "Nothing to land",
        body: `Every commit on this session's branch is already in ${b}.`,
        tone: "quiet",
        retry: false,
      };
    case "base_not_checked_out":
      return {
        title: `${b} isn't checked out in your checkout`,
        body: `Land merges into the branch your checkout has open. Check out ${b} there, then Land again.`,
        tone: "bad",
        retry: true,
      };
    case "primary_mid_merge":
      return {
        title: "Your checkout is mid-merge",
        body: "A merge or rebase is unfinished in your checkout. Finish or abort it there, then Land again.",
        tone: "bad",
        retry: true,
      };
    case "merge_failed":
      return {
        title: `Couldn't bring ${b} into this session`,
        body: "Git stopped the merge in the session's folder, so nothing changed.",
        tone: "bad",
        retry: true,
      };
    case "locked":
      return {
        title: "Land is busy right now",
        body: "Another operation is using this session's folder or your checkout. Nothing was merged — try again in a moment.",
        tone: "warn",
        retry: true,
      };
    case "base_moving":
      return {
        title: `${b} kept moving`,
        body: `New commits kept arriving on ${b} while Land ran, so nothing was merged. Land again in a moment.`,
        tone: "warn",
        retry: true,
      };
    case "primary_dirty":
      return {
        // Git names files the land would overwrite and, as well, any staged
        // file at all: it won't merge while the index differs from HEAD.
        title: "Your checkout has staged or uncommitted changes",
        body: named
          ? `Git won't land over ${named} in your checkout. Commit or stash ${files!.length === 1 ? "it" : "them"} there, then Land again.`
          : "Git won't land over staged or uncommitted changes in your checkout. Commit or stash them there, then Land again.",
        tone: "bad",
        retry: true,
      };
    case "primary_untracked":
      return {
        title: "Untracked files are in the way",
        body: named
          ? `Landing would overwrite ${named}, which ${files!.length === 1 ? "isn't" : "aren't"} tracked in your checkout. Move or commit ${files!.length === 1 ? "it" : "them"} there, then Land again.`
          : "Landing would overwrite untracked files in your checkout. Move or commit them there, then Land again.",
        tone: "bad",
        retry: true,
      };
    case "primary_refused":
      return {
        title: "Your checkout refused the merge",
        body: "Git aborted it, so your checkout is just as it was.",
        tone: "bad",
        retry: true,
      };
    default:
      return {
        title: "Land didn't run",
        body: "Nothing changed.",
        tone: "bad",
        retry: true,
      };
  }
}

/** The reasons whose detail is git talking — shown verbatim, monospace. */
export function refusalDetailIsGit(reason: string): boolean {
  return reason === "merge_failed" || reason === "primary_refused";
}

/** Why Undo didn't happen (POST /commit/undo's refusal reasons). */
export function undoRefusalCopy(reason: string | undefined): string {
  switch (reason) {
    case "not_tip":
      return "Can't undo: newer commits sit on top of it.";
    case "already_landed":
      return "Can't undo: it has already landed.";
    case "already_pushed":
      return "Can't undo: it has already been pushed.";
    case "mid_merge":
      return "Can't undo while the session's folder is mid-merge.";
    case "locked":
      return "Can't undo right now: another operation is using this session's folder. Try again in a moment.";
    case "merge_commit":
    case "not_session_commit":
      return "Can't undo: that isn't a commit this session made with Commit.";
    case "no_folder":
      return "Can't undo: this session has no git folder.";
    default:
      return "Couldn't undo that commit.";
  }
}

/** The message "Ask the agent to resolve" puts in the composer: an ordinary, editable user turn. */
export function resolveConflictPrompt(base: string | undefined, files: string[] | undefined): string {
  const b = base || "the base branch";
  const where = files && files.length > 0 ? ` in ${files.join(", ")}` : "";
  return `Merge ${b} into this branch and resolve the conflicts${where}, keeping both sides' changes. Then commit the merge.`;
}

/**
 * Land stopped after mergiraf merged files plain git would have left
 * conflicted: the base is merged in the session's branch, not yet in the
 * user's checkout. The title names the count, the body the files.
 */
export function autoResolvedCopy(base: string | undefined, files: string[]): { title: string; body: string } {
  const b = base || "The base branch";
  const named = nameFiles(files, 4);
  return {
    title: `${b} had moved on; mergiraf merged ${plural(files.length, "file")} automatically`,
    body: `${named ? `${named}. ` : ""}Check ${files.length === 1 ? "it" : "them"}, then Land again. Nothing has reached your checkout yet.`,
  };
}

/** The message "Ask the agent to check" puts in the composer: an ordinary, editable user turn. */
export function checkResolutionsPrompt(base: string | undefined, files: string[] | undefined): string {
  const b = base || "the base branch";
  const what = files && files.length > 0 ? files.join(", ") : "the files it merged";
  return `mergiraf just merged ${b} into this branch and resolved ${what} automatically. Check that each resolution is right (both sides' changes kept, the code still makes sense, it still builds and passes its tests), fix and commit anything that isn't, and tell me what you found.`;
}

/** Per-path reasons a Commit rejected a path (CommitPath.reason). */
export function commitPathReason(status: string, reason?: string): string {
  if (status === "already_committed") return "already committed";
  switch (reason) {
    case "outside_folder":
      return "outside this session's folder";
    case "not_session_file":
      return "not a file this session changed";
    case "pipeline_task_file":
      return "belongs to a pipeline task";
    default:
      return status === "rejected" ? "not committed" : "";
  }
}

function taskName(task: string | undefined): string {
  return task ? `“${task}”` : "a task";
}

/** The quiet card for D8's safety commit: the agent's edits went into a commit so a task could merge. */
export function pipelineSavedCopy(files: string[], task?: string): string {
  return `Pipeline saved your edits to ${nameFiles(files, 4) || "some files"} before merging ${taskName(task)}.`;
}

/** The warning for a task merge that went ahead mid-turn, naming what changed under the agent. */
export function pipelineMergedCopy(files: string[], task?: string): string {
  const changed = nameFiles(files, 6);
  return `A pipeline merged ${taskName(task)} into this folder while the agent was working${changed ? ` — changed: ${changed}` : ""}.`;
}
