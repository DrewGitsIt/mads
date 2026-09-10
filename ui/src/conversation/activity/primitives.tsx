import type { ToolKind, ToolStatus } from "../../router/types";
import styles from "./primitives.module.css";

/**
 * Vocabulary primitives (T11, mockup v3 §0): the per-kind icon tile, the
 * status glyph set (✓ ✕ ◔ ⊘ + running spinner), the diffstat chip, and the
 * MCP server badge. Everything downstream (rows, group headers, dock, task
 * cards) composes these rather than re-deriving glyphs.
 */

const KIND_GLYPHS: Record<ToolKind, string> = {
  read: "▤",
  search: "⌕",
  execute: "❯",
  edit: "±",
  delete: "✕",
  move: "⇄",
  fetch: "⇣",
  think: "◌",
  agent: "⛬",
  mcp: "◇",
  skill: "✦",
  plan: "≣",
  other: "∙",
};

export interface KindIconProps {
  kind: ToolKind;
  /** 22px default tile, or the 18px variant used in group-header kind sets. */
  size?: "md" | "sm";
}

export function KindIcon({ kind, size = "md" }: KindIconProps) {
  const cls = [styles.ki, styles[kind] ?? styles.other, size === "sm" ? styles.sm : ""]
    .filter(Boolean)
    .join(" ");
  return (
    <span className={cls} data-kind={kind} aria-hidden="true">
      {KIND_GLYPHS[kind] ?? KIND_GLYPHS.other}
    </span>
  );
}

const STATUS_GLYPHS: Record<Exclude<ToolStatus, "in_progress">, { glyph: string; cls: string; label: string }> = {
  completed: { glyph: "✓", cls: styles.stOk, label: "completed" },
  failed: { glyph: "✕", cls: styles.stBad, label: "failed" },
  pending: { glyph: "◔", cls: styles.stPend, label: "pending approval" },
  interrupted: { glyph: "⊘", cls: styles.stInt, label: "interrupted" },
};

export function StatusGlyph({ status }: { status: ToolStatus }) {
  if (status === "in_progress") return <Spinner />;
  const s = STATUS_GLYPHS[status];
  return (
    <span
      className={[styles.st, s.cls].join(" ")}
      role="img"
      aria-label={s.label}
      data-status={status}
    >
      {s.glyph}
    </span>
  );
}

export function Spinner() {
  return <span className={styles.spinner} role="img" aria-label="in progress" data-status="in_progress" />;
}

export function Diffstat({ adds, dels }: { adds: number; dels: number }) {
  if (adds <= 0 && dels <= 0) return null;
  return (
    <span className={styles.diffstat}>
      {adds > 0 && <b className={styles.diffstatAdd}>+{adds}</b>}
      {dels > 0 && <b className={styles.diffstatDel}>−{dels}</b>}
    </span>
  );
}

export function McpBadge({ server }: { server: string }) {
  return <span className={styles.badge}>{server}</span>;
}
