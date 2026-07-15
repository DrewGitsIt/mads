// Notification model: badge-only at launch (REBUILD-SPEC.md §8 req. 5 —
// passive, no interrupt). A future in-app inbox would likely reuse
// `attentionCount`'s per-session derivation as its item source rather than a
// new backend feed; no backend schema changes are anticipated.

import type { Session, Task } from "../../router/types";

export type CommandKind =
  | "jump-session"
  | "start-pipeline"
  | "open-task"
  | "new-session"
  | "run-retro"
  | "toggle-shell"
  | "open-settings";

export interface Command {
  id: string;
  kind: CommandKind;
  label: string;
  detail?: string;
  sessionId?: string;
  taskId?: string;
}

const STATIC_COMMANDS: Command[] = [
  { id: "action:new-session", kind: "new-session", label: "New session" },
  { id: "action:run-retro", kind: "run-retro", label: "Run retro" },
  { id: "action:toggle-shell", kind: "toggle-shell", label: "Toggle shell" },
  { id: "action:open-settings", kind: "open-settings", label: "Open settings" },
];

/**
 * Sessions first (jump + start-pipeline pair per session, since the palette
 * has no "current session" concept — see HANDOFF §4), then tasks (dispatch
 * reuses jump-to-session, no standalone task detail route exists), then the
 * fixed action set.
 */
export function buildCommands(sessions: Session[], tasks: Task[]): Command[] {
  const sessionCommands: Command[] = [];
  for (const session of sessions) {
    sessionCommands.push({
      id: `jump:${session.id}`,
      kind: "jump-session",
      label: `Jump to · ${session.name}`,
      detail: session.workspace,
      sessionId: session.id,
    });
    sessionCommands.push({
      id: `start-pipeline:${session.id}`,
      kind: "start-pipeline",
      label: `Start pipeline · ${session.name}`,
      detail: session.workspace,
      sessionId: session.id,
    });
  }

  const sessionsById = new Map(sessions.map((s) => [s.id, s]));
  const taskCommands: Command[] = tasks.map((task) => ({
    id: `task:${task.id}`,
    kind: "open-task",
    label: task.title,
    detail: sessionsById.get(task.session_id)?.name,
    sessionId: task.session_id,
    taskId: task.id,
  }));

  return [...sessionCommands, ...taskCommands, ...STATIC_COMMANDS];
}

/** Case-insensitive substring match against label + detail — no fuzzy scoring. */
export function filterCommands(commands: Command[], query: string): Command[] {
  const q = query.trim().toLowerCase();
  if (q === "") return commands;
  return commands.filter(
    (c) => c.label.toLowerCase().includes(q) || (c.detail?.toLowerCase().includes(q) ?? false),
  );
}
