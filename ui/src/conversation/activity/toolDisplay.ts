import type { ToolDiff, ToolKind, ToolStatus, ToolUse } from "../../router/types";

/**
 * Shared display derivations for tool calls — the single place the activity
 * UI (ActivityGroup rows, the bottom dock, ToolCard in the pipeline drill-in)
 * turns an enriched ToolUse into something renderable. Server-built fields
 * (kind/title/status, T4) always win; the name-based fallbacks below only
 * cover events persisted before enrichment landed.
 */

// Fallback name→kind mapping, mirroring providers/claudecode's server-side
// table — used only when the wire didn't stamp `kind`.
export function kindOf(tool: ToolUse): ToolKind {
  if (tool.kind) return tool.kind;
  switch (tool.name) {
    case "Bash":
      return "execute";
    case "Grep":
    case "Glob":
    case "WebSearch":
      return "search";
    case "Read":
      return "read";
    case "Write":
    case "Edit":
    case "MultiEdit":
    case "NotebookEdit":
      return "edit";
    case "WebFetch":
      return "fetch";
    case "Task":
    case "Agent":
      return "agent";
    case "Skill":
      return "skill";
    case "TodoWrite":
      return "plan";
  }
  if (tool.name.startsWith("mcp__")) return "mcp";
  return "other";
}

/**
 * For an execute-kind call with the Bash input shape ({command,
 * description?, …}): the bare command string plus any remaining option
 * keys. The description already renders as the row/dock title, and JSON
 * braces around a shell line only hurt reading — detail surfaces show the
 * command as-is and drop the description instead of echoing the raw input.
 */
export function commandInput(
  tool: ToolUse,
): { command: string; options?: Record<string, unknown> } | undefined {
  if (kindOf(tool) !== "execute") return undefined;
  if (!tool.input || typeof tool.input !== "object") return undefined;
  const { command, ...rest } = tool.input as Record<string, unknown>;
  delete rest.description;
  if (typeof command !== "string" || command === "") return undefined;
  return Object.keys(rest).length > 0 ? { command, options: rest } : { command };
}

/** MCP server name — the enriched `server` field, else parsed from `mcp__server__tool`. */
export function mcpServerOf(tool: ToolUse): string | undefined {
  if (tool.server) return tool.server;
  if (tool.name.startsWith("mcp__")) return tool.name.split("__")[1] || undefined;
  return undefined;
}

/**
 * One line of the most relevant input for a row/header, per tool. Retained
 * ONLY as the fallback for events persisted before the server started
 * building `title` (T4) — prefer {@link toolTitle}.
 */
export function summarize(tool: ToolUse): string {
  const input =
    tool.input && typeof tool.input === "object" ? (tool.input as Record<string, unknown>) : {};
  const str = (v: unknown) => (typeof v === "string" && v.trim() !== "" ? v : undefined);
  switch (tool.name) {
    case "Bash":
      return str(input.description) ?? str(input.command) ?? "no input";
    case "Grep":
    case "Glob": {
      const pattern = str(input.pattern);
      if (pattern) {
        const where = str(input.path);
        return where ? `${pattern} in ${where}` : pattern;
      }
      break;
    }
    case "Task":
      if (str(input.description)) return input.description as string;
      break;
    case "WebFetch":
      if (str(input.url)) return input.url as string;
      break;
    case "WebSearch":
      if (str(input.query)) return input.query as string;
      break;
  }
  if (tool.path) return tool.path;
  const first = Object.entries(input)[0];
  if (first) return `${first[0]}: ${String(first[1]).slice(0, 60)}`;
  return "no input";
}

/** The server-built one-line summary, falling back to the legacy client derivation. */
export function toolTitle(tool: ToolUse): string {
  return tool.title ?? summarize(tool);
}

/**
 * Effective lifecycle status for rendering. Precedence: an out-of-band
 * status change (session.tool_status_changed, merged by id) > the stamped
 * wire status > result-derived > honesty fallback — a live call without a
 * result is in progress; a historical one is ⊘ interrupted (the turn ended
 * and no result ever arrived), never silently blank.
 */
export function statusOf(
  tool: ToolUse,
  override?: ToolStatus,
  live?: boolean,
): ToolStatus {
  const stated = override ?? tool.status;
  // A result in hand beats a stale non-terminal status (e.g. an
  // in_progress stamped at tool_use time that never got a follow-up).
  if (tool.result && (!stated || stated === "in_progress" || stated === "pending")) {
    return tool.result.is_error ? "failed" : "completed";
  }
  if (stated) return stated;
  if (tool.result) return tool.result.is_error ? "failed" : "completed";
  return live ? "in_progress" : "interrupted";
}

export interface DiffHunk {
  removed: string[];
  added: string[];
}

/**
 * A file-mutating tool (Write/Edit/MultiEdit/NotebookEdit) carries its change
 * as before/after text in the tool input — turn that into diff hunks. Returns
 * null for every other tool. This is the input-derived fallback; the dock
 * prefers the shadow store's historically-accurate tool-diff when available.
 */
export function diffHunks(tool: ToolUse): DiffHunk[] | null {
  if (!tool.input || typeof tool.input !== "object") return null;
  const o = tool.input as Record<string, unknown>;
  const lines = (v: unknown) => (typeof v === "string" ? v.split("\n") : []);

  switch (tool.name) {
    case "Write":
      return typeof o.content === "string" ? [{ removed: [], added: lines(o.content) }] : null;
    case "NotebookEdit":
      return typeof o.new_source === "string" ? [{ removed: [], added: lines(o.new_source) }] : null;
    case "Edit":
      if (typeof o.old_string !== "string" && typeof o.new_string !== "string") return null;
      return [{ removed: lines(o.old_string), added: lines(o.new_string) }];
    case "MultiEdit":
      if (!Array.isArray(o.edits)) return null;
      return (o.edits as Array<Record<string, unknown>>).map((e) => ({
        removed: lines(e.old_string),
        added: lines(e.new_string),
      }));
    default:
      return null;
  }
}

export function diffStat(hunks: DiffHunk[]): { adds: number; dels: number } {
  return hunks.reduce(
    (acc, h) => ({ adds: acc.adds + h.added.length, dels: acc.dels + h.removed.length }),
    { adds: 0, dels: 0 },
  );
}

/**
 * Turns the tool-diff endpoint's whole-file before/after into a single
 * changed-region hunk by trimming the common prefix/suffix lines — honest
 * and cheap (no O(n²) diff), at the cost of merging multiple hunks into one
 * changed span.
 */
export function serverDiffHunk(diff: Pick<ToolDiff, "original" | "modified">): DiffHunk {
  const a = diff.original === "" ? [] : diff.original.split("\n");
  const b = diff.modified === "" ? [] : diff.modified.split("\n");
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  return { removed: a.slice(start, endA), added: b.slice(start, endB) };
}

export function formatDuration(startIso?: string, endIso?: string): string | undefined {
  if (!startIso || !endIso) return undefined;
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return undefined;
  return formatMs(ms);
}

export function formatMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${String(Math.round(s % 60)).padStart(2, "0")}s`;
}

export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  return `${(n / 1000).toFixed(1)}k`;
}

export function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? path : path.slice(i + 1);
}

/** The most salient file position a call touched — first enriched location, else the legacy path. */
export function primaryLocation(tool: ToolUse): string | undefined {
  return tool.locations?.[0]?.path ?? tool.path;
}
