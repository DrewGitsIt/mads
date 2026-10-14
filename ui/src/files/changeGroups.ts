// Group builders for the Changes tab's Tier, Turn and Commit groupings
// (Folder is a tree — see folderTree.ts). Inside every group files sort by
// tier, then size: tier stays the backbone whatever the grouping.
import { CHANGE_TIERS, type ChangeCommit, type ChangeTier, type ChangeTurn, type ChangedFile } from "../router/types";
import { TIER_DESC, TIER_LABEL, sortTierThenSize } from "./changesModel";

export interface FileEntry {
  file: ChangedFile;
  /** Unique within the list: a file can appear under several turns or commits. */
  key: string;
  /** Set under a commit: selecting the entry shows that commit's diff. */
  commit?: string;
  /** Set under a turn. */
  turn?: number;
}

export interface ChangeGroup {
  /** Stable open/closed state key. */
  key: string;
  kind: "tier" | "turn" | "commit" | "uncommitted";
  title: string;
  /** Turn grouping: the turn's prompt (full; the view clips it). Empty string = harness-started turn. */
  prompt?: string;
  /** Commit grouping: the commit hash. */
  hash?: string;
  tier?: ChangeTier;
  turn?: number;
  desc?: string;
  /** Collapsed until the user opens it (Generated and Scratch). */
  defaultOpen: boolean;
  entries: FileEntry[];
}

function entries(files: ChangedFile[], keyPrefix: string, extra: Partial<FileEntry> = {}): FileEntry[] {
  return sortTierThenSize(files).map((file) => ({ file, key: keyPrefix + file.path, ...extra }));
}

/** One section per non-empty tier, in tier order. */
export function buildTierGroups(files: ChangedFile[]): ChangeGroup[] {
  const groups: ChangeGroup[] = [];
  for (const tier of CHANGE_TIERS) {
    const fs = files.filter((f) => f.tier === tier);
    if (!fs.length) continue;
    groups.push({
      key: `t:${tier}`,
      kind: "tier",
      tier,
      title: TIER_LABEL[tier],
      desc: TIER_DESC[tier],
      defaultOpen: tier !== "generated" && tier !== "scratch",
      entries: entries(fs, ""),
    });
  }
  return groups;
}

/** One section per turn, zero-file turns included (the view says "no file changes"). */
export function buildTurnGroups(files: ChangedFile[], turns: ChangeTurn[]): ChangeGroup[] {
  return turns.map((t) => ({
    key: `u:${t.n}`,
    kind: "turn" as const,
    turn: t.n,
    title: `Turn ${t.n}`,
    prompt: t.prompt,
    defaultOpen: true,
    entries: entries(
      files.filter((f) => f.turns.includes(t.n)),
      `u${t.n}:`,
      { turn: t.n },
    ),
  }));
}

/**
 * Uncommitted first (when any file is), then commits newest first; a file
 * in two commits appears under each. Commits with no listed file are left
 * out — their files no longer differ from the baseline.
 */
export function buildCommitGroups(files: ChangedFile[], commits: ChangeCommit[]): ChangeGroup[] {
  const groups: ChangeGroup[] = [];
  const unc = files.filter((f) => f.uncommitted);
  if (unc.length) {
    groups.push({
      key: "c:uncommitted",
      kind: "uncommitted",
      title: "Uncommitted",
      desc: "Working tree vs. the last commit",
      defaultOpen: true,
      entries: entries(unc, "c:"),
    });
  }
  // ChangeSet.commits arrive newest first.
  for (const c of commits) {
    const fs = files.filter((f) => f.commits.includes(c.hash));
    if (!fs.length) continue;
    groups.push({
      key: `c:${c.hash}`,
      kind: "commit",
      hash: c.hash,
      title: c.message,
      defaultOpen: true,
      entries: entries(fs, `c${c.hash}:`, { commit: c.hash }),
    });
  }
  return groups;
}
