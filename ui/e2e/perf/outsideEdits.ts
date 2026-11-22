import { existsSync, readFileSync, realpathSync, rmSync, writeFileSync, appendFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import type { Page } from "@playwright/test";
import type { GitFixture, GitSession } from "./scenarios";
import { fxGit } from "./stack";

/**
 * Edits made straight in a fixture checkout, as an editor or a terminal
 * would: no agent, so no file.changed and no ledger row. Check i and
 * scenarios 9g/9h (docs/PERF-HARNESS-PLAN.md). Every path is checked to be
 * inside the run's git fixture (its temp tree) before it is touched.
 */

/**
 * How long after the last refresh a focus is guaranteed to refresh again:
 * the UI skips a focus within 30 s of its last finished recount (Phase A,
 * worktreeRefresh.ts WORKTREE_REFRESH_FOCUS_SKIP_MS), and B's bodiless
 * refresh and first change-set read recompute only a change set older than
 * 30 s. 30 s + 1 s of margin (PERF_FOCUS_SKIP_MS).
 */
export const FOCUS_SKIP_MS = Number(process.env.PERF_FOCUS_SKIP_MS ?? 31_000);

/**
 * How long the Changes tab may take to show an outside edit after the focus
 * (or the reopen) that should pick it up (PERF_OUTSIDE_S, default 10). B:
 * a 202, the heavy session's change set recomputed (~1 s on b2), the batch
 * announced, a cached GET. Main/A: the reopen's GET computes the change set
 * (3.6–6.7 s measured on the heavy session under load) behind the mount's
 * events barrier. 10 s ≈ main's worst compute plus render, with margin; no
 * repo-watcher term (check f's 15 s has one): the trigger here is explicit.
 */
export const OUTSIDE_S = Number(process.env.PERF_OUTSIDE_S ?? 10);

/** The real path of p (or of its parent, for a file not there yet), refused unless it's inside the fixture's root. */
export function inFixture(g: GitFixture, p: string): string {
  const root = realpathSync(g.root);
  const real = existsSync(p) ? realpathSync(p) : path.join(realpathSync(path.dirname(p)), path.basename(p));
  const liveRepos = path.join(os.homedir(), "repo") + path.sep;
  if (!real.startsWith(root + path.sep) || real.startsWith(liveRepos) || root.startsWith(liveRepos)) {
    throw new Error(`refusing to edit ${real}: not inside the run's git fixture ${root}`);
  }
  return real;
}

/** Blur, visibilitychange, focus: the window coming back (as 9d). */
export function focusWindow(page: Page): Promise<void> {
  return page.evaluate(() => {
    window.dispatchEvent(new Event("blur"));
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("focus"));
  });
}

/** The Changes tab's totals line: "N files · +A −D". */
export interface Totals {
  files?: number;
  added?: number;
  deleted?: number;
}

export async function readTotals(page: Page): Promise<Totals> {
  return page.evaluate(() => {
    const t = document.querySelector('#workspace-panel-files [data-testid="changes-totals"]')?.textContent ?? "";
    const num = (re: RegExp) => {
      const m = re.exec(t);
      return m ? Number(m[1].replace(/[,\s]/g, "")) : undefined;
    };
    return { files: num(/^\s*([\d,]+) files?/), added: num(/\+([\d,]+)/), deleted: num(/[−-]([\d,]+)/) };
  });
}

export const sameTotals = (a: Totals, b: Totals) => a.files === b.files && a.added === b.added && a.deleted === b.deleted;
export const showTotals = (t: Totals) => `${t.files ?? "–"} files +${t.added ?? "–"} −${t.deleted ?? "–"}`;

const lines = (text: string) => (text === "" ? 0 : text.split("\n").length - (text.endsWith("\n") ? 1 : 0));
const isText = (b: Buffer) => !b.includes(0);

/** Tracked files with unstaged worktree edits (already dirty), text only, sorted; read without taking git's optional locks. */
export function dirtyTracked(g: GitFixture, s: GitSession): string[] {
  const top = fxGit(s.folder, ["--no-optional-locks", "rev-parse", "--show-toplevel"]);
  const out = fxGit(s.folder, ["--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=no"]);
  return out
    .split("\0")
    .filter((e) => e.length > 3 && e[0] === " " && e[1] === "M")
    .map((e) => path.join(top, e.slice(3)))
    .filter((p) => isText(readFileSync(inFixture(g, p))))
    .sort();
}

/** An outside edit: does it, and says how a change set's totals should move. */
export interface OutsideEdit {
  what: string;
  path: string;
  delta: Totals;
}

export function addUntracked(g: GitFixture, s: GitSession, name: string): OutsideEdit {
  const p = inFixture(g, path.join(s.new_dir, name));
  const body = "# perf outside edit\n\nmade outside mads\n";
  writeFileSync(p, body);
  return { what: "new untracked file", path: p, delta: { files: 1, added: lines(body), deleted: 0 } };
}

/** Appends two lines to an already-dirty file: content and size change. */
export function reEdit(g: GitFixture, p: string): OutsideEdit {
  const real = inFixture(g, p);
  const nl = readFileSync(real, "utf8").endsWith("\n");
  appendFileSync(real, `${nl ? "" : "\n"}// perf outside re-edit 1\n// perf outside re-edit 2\n`);
  return { what: "re-edit of a dirty file (size changes)", path: real, delta: nl ? { files: 0, added: 2, deleted: 0 } : { files: 0, added: 3, deleted: 1 } };
}

/**
 * Changes one letter of the first line of an already-dirty file to the next
 * letter: same byte size, new content and mtime. The line is one the file's
 * dirty edits (appended) didn't touch, so it reads as one more line changed.
 */
export function sameSizeEdit(g: GitFixture, p: string): OutsideEdit {
  const real = inFixture(g, p);
  const b = readFileSync(real);
  const before = b.length;
  const end = b.indexOf(0x0a) < 0 ? b.length : b.indexOf(0x0a);
  let at = -1;
  for (let i = 0; i < end; i++) {
    const c = b[i];
    if ((c >= 0x61 && c < 0x7a) || (c >= 0x41 && c < 0x5a)) {
      at = i;
      break;
    }
  }
  if (at < 0) throw new Error(`${real}: no letter on its first line to change`);
  b[at] += 1;
  writeFileSync(real, b);
  if (readFileSync(real).length !== before) throw new Error(`${real}: the same-size edit changed its size`);
  return { what: "same-size re-edit of a dirty file", path: real, delta: { files: 0, added: 1, deleted: 1 } };
}

/**
 * A tracked text file the change set doesn't list (unchanged since session
 * start): deleting it is one more file. The first in path order.
 */
export function unlistedTracked(g: GitFixture, s: GitSession, listed: Set<string>): string {
  const top = fxGit(s.folder, ["--no-optional-locks", "rev-parse", "--show-toplevel"]);
  const tracked = fxGit(top, ["--no-optional-locks", "ls-files", "-z"]).split("\0").filter(Boolean).sort();
  for (const rel of tracked) {
    const p = path.join(top, rel);
    if (listed.has(p) || listed.has(realpathSync(p))) continue;
    if (isText(readFileSync(inFixture(g, p)))) return p;
  }
  throw new Error(`no tracked file outside the change set in ${top}`);
}

/** Deletes a tracked file the session hasn't changed. */
export function deleteTracked(g: GitFixture, p: string): OutsideEdit {
  const real = inFixture(g, p);
  const n = lines(readFileSync(real, "utf8"));
  rmSync(real);
  return { what: "deleted tracked file", path: real, delta: { files: 1, added: 0, deleted: n } };
}

export const plus = (a: Totals, d: Totals): Totals => ({
  files: (a.files ?? 0) + (d.files ?? 0),
  added: (a.added ?? 0) + (d.added ?? 0),
  deleted: (a.deleted ?? 0) + (d.deleted ?? 0),
});
