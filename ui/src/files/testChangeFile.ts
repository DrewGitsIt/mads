import type { ChangedFile } from "../router/types";

/** A ChangedFile for tests: `path` is the display path, the absolute one is under /w/. */
export function file(path: string, over: Partial<ChangedFile> = {}): ChangedFile {
  return {
    path: `/w/${path}`,
    display: path,
    change: "modified",
    added: 1,
    deleted: 0,
    tier: "source",
    attributed: true,
    turns: [],
    commits: [],
    uncommitted: true,
    last_changed: "2026-10-14T10:00:00Z",
    ...over,
  };
}
