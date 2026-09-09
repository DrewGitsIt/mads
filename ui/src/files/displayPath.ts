/**
 * Repo-relative display form of a change path. The ledger records absolute
 * worktree paths, so raw rendering plus end-truncation collapses every row
 * to the same "/home/drew/…" prefix — strip the session's worktree_dir when
 * the path is under it; otherwise collapse a leading home dir to "~".
 * Already-relative paths pass through unchanged.
 */
export function displayPath(path: string, worktreeDir?: string): string {
  if (worktreeDir) {
    const dir = worktreeDir.endsWith("/") ? worktreeDir.slice(0, -1) : worktreeDir;
    if (dir && path.startsWith(dir + "/")) return path.slice(dir.length + 1);
  }
  const home = /^(\/home\/[^/]+|\/Users\/[^/]+)(?=\/|$)/.exec(path);
  if (home) return "~" + path.slice(home[0].length);
  return path;
}
