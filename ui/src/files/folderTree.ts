// The Changes tab's Folder grouping: a standard file tree with compact
// single-child folder chains ("internal/auth" rather than internal › auth),
// folders first then files, alphabetical, and per-folder rollups. Files
// outside the session folder sit under their own root, placed last.
import type { ChangedFile } from "../router/types";
import { basename, isLowTier } from "./changesModel";

export const OUTSIDE_ROOT = "Outside the session folder";

export interface FolderNode {
  /** Display name — possibly a compacted chain like "internal/auth". */
  name: string;
  /** Stable key for open/closed state: the full path from the tree root. */
  key: string;
  dirs: FolderNode[];
  files: ChangedFile[];
  /** Files anywhere under this folder. */
  count: number;
  added: number;
  deleted: number;
  /** Every file under it is generated or scratch: starts collapsed and dimmed. */
  low: boolean;
  /** The "Outside the session folder" root. */
  outside?: boolean;
  /** Every file under this folder (for unseen rollups). */
  all: ChangedFile[];
}

interface Building {
  name: string;
  dirs: Map<string, Building>;
  files: ChangedFile[];
  outside?: boolean;
}

function segments(f: ChangedFile): string[] {
  if (f.outside) return [OUTSIDE_ROOT, ...f.display.replace(/^\/+/, "").split("/").filter(Boolean)];
  return f.display.split("/").filter(Boolean);
}

function compact(n: Building): Building {
  let c = n;
  while (!c.outside && c.files.length === 0 && c.dirs.size === 1) {
    const only = [...c.dirs.values()][0];
    c = { ...only, name: `${c.name}/${only.name}` };
  }
  return c;
}

function finish(n: Building, parentKey: string): FolderNode {
  const key = parentKey ? `${parentKey}/${n.name}` : n.name;
  const dirs = [...n.dirs.values()]
    .map(compact)
    .sort((a, b) => Number(!!a.outside) - Number(!!b.outside) || a.name.localeCompare(b.name))
    .map((d) => finish(d, key));
  const files = n.files.slice().sort((a, b) => basename(a.display).localeCompare(basename(b.display)));
  const all = [...files, ...dirs.flatMap((d) => d.all)];
  return {
    name: n.name,
    key,
    dirs,
    files,
    count: all.length,
    added: all.reduce((s, f) => s + f.added, 0),
    deleted: all.reduce((s, f) => s + f.deleted, 0),
    low: all.length > 0 && all.every(isLowTier),
    outside: n.outside,
    all,
  };
}

/** The tree's root: an unnamed node whose dirs and files are the top level. */
export function buildFolderTree(files: ChangedFile[]): FolderNode {
  const root: Building = { name: "", dirs: new Map(), files: [] };
  for (const f of files) {
    const parts = segments(f);
    let n = root;
    parts.slice(0, -1).forEach((seg, i) => {
      let next = n.dirs.get(seg);
      if (!next) {
        next = { name: seg, dirs: new Map(), files: [], outside: f.outside && i === 0 ? true : undefined };
        n.dirs.set(seg, next);
      }
      n = next;
    });
    n.files.push(f);
  }
  return finish(root, "");
}
