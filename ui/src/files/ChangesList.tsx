import type { ReactNode } from "react";
import { Button } from "../components/Button/Button";
import type { ChangedFile } from "../router/types";
import { FileCounts, KindBadge, LineCounts, NewDot, SizeBar } from "./ChangeBits";
import type { ChangeGroup, FileEntry } from "./changeGroups";
import { basename, clipPrompt, dirname, isLowTier, sumLines } from "./changesModel";
import type { FolderNode } from "./folderTree";
import styles from "./ChangesList.module.css";

/** What a row's click selects: the file, plus the commit whose diff it shows (Commit grouping) and the turn it sits under. */
export interface ChangeSelection {
  path: string;
  commit?: string;
  turn?: number;
}

export function entryMatches(sel: ChangeSelection | null, e: Pick<FileEntry, "commit" | "turn"> & { path: string }): boolean {
  if (!sel || sel.path !== e.path) return false;
  if ((sel.commit ?? "") !== (e.commit ?? "")) return false;
  return sel.turn === undefined || e.turn === undefined || sel.turn === e.turn;
}

export interface RowContext {
  selection: ChangeSelection | null;
  onSelect: (sel: ChangeSelection, file: ChangedFile) => void;
  isUnseen: (f: ChangedFile) => boolean;
  isOpen: (key: string, dflt: boolean) => boolean;
  toggle: (key: string, dflt: boolean) => void;
}

function FileRow({ entry, ctx }: { entry: FileEntry; ctx: RowContext }) {
  const f = entry.file;
  const dir = dirname(f.display);
  return (
    <button
      type="button"
      className={styles.row}
      data-dim={isLowTier(f) || undefined}
      data-selected={entryMatches(ctx.selection, { ...entry, path: f.path })}
      aria-current={entryMatches(ctx.selection, { ...entry, path: f.path }) || undefined}
      title={f.path}
      data-path={f.path}
      data-commit={entry.commit}
      onClick={() => ctx.onSelect({ path: f.path, commit: entry.commit, turn: entry.turn }, f)}
    >
      <NewDot show={ctx.isUnseen(f)} />
      <KindBadge change={f.change} />
      <span className={styles.p}>
        <span className={styles.fn}>{basename(f.display)}</span>
        {dir && (
          // RTL context puts the ellipsis on the start side so the folder
          // nearest the file survives; the inner bdi restores LTR order.
          <span className={styles.dir}>
            <bdi>{dir}</bdi>
          </span>
        )}
      </span>
      <span className={styles.meta}>
        <FileCounts file={f} />
        <SizeBar added={f.added} deleted={f.deleted} />
      </span>
    </button>
  );
}

export function GroupSection({
  group,
  ctx,
  extra,
}: {
  group: ChangeGroup;
  ctx: RowContext;
  extra?: ReactNode;
}) {
  const files = group.entries.map((e) => e.file);
  const { added, deleted } = sumLines(files);
  const unseen = files.some(ctx.isUnseen);
  const sub =
    group.kind === "turn" ? (
      group.prompt ? (
        <span className={styles.gp} title={group.prompt}>
          “{clipPrompt(group.prompt)}”
        </span>
      ) : null
    ) : group.kind === "commit" ? (
      <span className={styles.hash}>{group.hash?.slice(0, 7)}</span>
    ) : null;

  const dataAttrs = {
    "data-group-key": group.key,
    "data-turn": group.turn,
  };

  if (group.kind === "turn" && files.length === 0) {
    return (
      <div className={styles.grp} {...dataAttrs}>
        <div className={styles.gh}>
          <span className={styles.ghStatic}>
            <span className={styles.caret} />
            <span className={styles.name}>{group.title}</span>
            {sub}
          </span>
          <span className={styles.n}>no file changes</span>
        </div>
      </div>
    );
  }

  const open = ctx.isOpen(group.key, group.defaultOpen);
  return (
    <div className={styles.grp} {...dataAttrs}>
      <div className={styles.gh}>
        <button
          type="button"
          className={styles.ghToggle}
          aria-expanded={open}
          onClick={() => ctx.toggle(group.key, group.defaultOpen)}
        >
          <span className={styles.caret}>{open ? "▾" : "▸"}</span>
          <span className={group.kind === "commit" ? styles.nameCommit : styles.name} title={group.title}>
            {group.title}
          </span>
          {sub}
          <span className={styles.n}>{files.length}</span>
          <NewDot show={unseen} className={styles.ghDot} />
        </button>
        {extra}
        <LineCounts added={added} deleted={deleted} />
      </div>
      {open && group.desc && <div className={styles.gdesc}>{group.desc}</div>}
      {open && group.entries.map((e) => <FileRow key={e.key} entry={e} ctx={ctx} />)}
    </div>
  );
}

export function GroupList({
  groups,
  ctx,
  onDeleteScratch,
}: {
  groups: ChangeGroup[];
  ctx: RowContext;
  onDeleteScratch?: (files: ChangedFile[]) => void;
}) {
  return (
    <>
      {groups.map((g) => (
        <GroupSection
          key={g.key}
          group={g}
          ctx={ctx}
          extra={
            g.tier === "scratch" && onDeleteScratch ? (
              <Button
                variant="secondary"
                size="sm"
                className={styles.danger}
                onClick={() => onDeleteScratch(g.entries.map((e) => e.file))}
              >
                Delete {g.entries.length}…
              </Button>
            ) : undefined
          }
        />
      ))}
    </>
  );
}

function Guides({ depth }: { depth: number }) {
  return (
    <>
      {Array.from({ length: depth }, (_, i) => (
        <span key={i} className={styles.guide} />
      ))}
    </>
  );
}

function FolderRows({ node, depth, ctx }: { node: FolderNode; depth: number; ctx: RowContext }) {
  return (
    <>
      {node.dirs.map((d) => {
        const key = `f:${d.key}`;
        const open = ctx.isOpen(key, !d.low);
        return (
          <div key={d.key} role="group">
            <button
              type="button"
              className={styles.dirrow}
              data-dim={d.low || undefined}
              data-outside={d.outside || undefined}
              aria-expanded={open}
              onClick={() => ctx.toggle(key, !d.low)}
            >
              <span className={styles.ind}>
                <Guides depth={depth} />
                <span className={styles.tcaret}>{open ? "▾" : "▸"}</span>
              </span>
              <span className={styles.nm}>
                <span className={styles.nmText}>{d.name}</span>
                <span className={styles.cnt}>{d.count}</span>
                <NewDot show={d.all.some(ctx.isUnseen)} className={styles.ghDot} />
              </span>
              <span className={styles.meta}>
                <LineCounts added={d.added} deleted={d.deleted} />
                <SizeBar added={d.added} deleted={d.deleted} />
              </span>
            </button>
            {open && <FolderRows node={d} depth={depth + 1} ctx={ctx} />}
          </div>
        );
      })}
      {node.files.map((f) => (
        <button
          key={f.path}
          type="button"
          className={styles.trow}
          data-dim={isLowTier(f) || undefined}
          data-selected={entryMatches(ctx.selection, { path: f.path })}
          aria-current={entryMatches(ctx.selection, { path: f.path }) || undefined}
          title={f.path}
          data-path={f.path}
          onClick={() => ctx.onSelect({ path: f.path }, f)}
        >
          <span className={styles.ind}>
            <Guides depth={depth} />
            <NewDot show={ctx.isUnseen(f)} className={styles.tcaret} />
            <KindBadge change={f.change} />
          </span>
          <span className={styles.fname}>{basename(f.display)}</span>
          <span className={styles.meta}>
            <FileCounts file={f} />
            <SizeBar added={f.added} deleted={f.deleted} />
          </span>
        </button>
      ))}
    </>
  );
}

export function FolderTreeView({ tree, ctx }: { tree: FolderNode; ctx: RowContext }) {
  return (
    <div className={styles.tree}>
      <FolderRows node={tree} depth={0} ctx={ctx} />
    </div>
  );
}

