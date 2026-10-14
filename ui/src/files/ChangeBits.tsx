// Small pieces every Changes row shares — the tab's list, tree, detail
// header and the rail: the new-since-you-looked dot, the A/M/D badge, the
// +N −M counts and the size bar.
import type { ChangedFile } from "../router/types";
import { KIND_LETTER, sizeBarWidth } from "./changesModel";
import styles from "./ChangeBits.module.css";

export function NewDot({ show, className }: { show: boolean; className?: string }) {
  if (!show) return <span className={className} />;
  return (
    <span className={className}>
      <span className={styles.dot} title="New since you looked" aria-label="new since you looked" />
    </span>
  );
}

export function KindBadge({ change }: { change: ChangedFile["change"] }) {
  const k = KIND_LETTER[change];
  return (
    <span className={styles.kind} data-kind={k} title={change}>
      {k}
    </span>
  );
}

/** +N −M, or "bin" / "—" when the counts mean nothing. */
export function LineCounts({
  added,
  deleted,
  binary,
  unknown,
}: {
  added: number;
  deleted: number;
  binary?: boolean;
  unknown?: boolean;
}) {
  if (binary) return <span className={styles.num} title="Binary file">bin</span>;
  if (unknown) return <span className={styles.num} title="Line counts unavailable">—</span>;
  return (
    <span className={styles.num}>
      <span className={styles.add}>+{added.toLocaleString()}</span>{" "}
      <span className={styles.del}>−{deleted.toLocaleString()}</span>
    </span>
  );
}

export function FileCounts({ file }: { file: ChangedFile }) {
  return (
    <LineCounts added={file.added} deleted={file.deleted} binary={file.binary} unknown={file.counts_unknown} />
  );
}

export function SizeBar({ added, deleted }: { added: number; deleted: number }) {
  const total = added + deleted;
  return (
    <span className={styles.size} style={{ width: sizeBarWidth(added, deleted) }} aria-hidden="true">
      <i className={styles.a} style={{ width: total ? `${(100 * added) / total}%` : 0 }} />
      <i className={styles.d} style={{ width: total ? `${(100 * deleted) / total}%` : 0 }} />
    </span>
  );
}
