import { compactCommand } from "./compact";
import { exportCommand } from "./export";
import { modelCommand } from "./model";
import { newCommand } from "./new";
import { newFollowerCommand } from "./newFollower";
import { newLeaderCommand } from "./newLeader";
import { newMonitorCommand } from "./newMonitor";
import { taskCommand } from "./task";
import { usageCommand } from "./usage";
import type {
  CommandAvailability,
  CommandContext,
  CommandDescriptor,
} from "./types";

/**
 * Every "/" command, in menu order. Adding a command is one descriptor file
 * under commands/ plus one line here. Anything typed after "/" that isn't a
 * (visible) name or alias here is sent to the harness as plain text.
 */
export const COMMANDS: CommandDescriptor[] = [
  taskCommand,
  modelCommand,
  exportCommand,
  compactCommand,
  usageCommand,
  newCommand,
  newLeaderCommand,
  newFollowerCommand,
  newMonitorCommand,
];

function visible(
  commands: CommandDescriptor[],
  ctx: CommandContext,
): CommandDescriptor[] {
  return commands.filter((c) => c.visible?.(ctx) ?? true);
}

/** The visible command named or aliased `name` (case-insensitive), if any. */
export function findCommand(
  name: string,
  ctx: CommandContext,
  commands: CommandDescriptor[] = COMMANDS,
): CommandDescriptor | undefined {
  const n = name.toLowerCase();
  return visible(commands, ctx).find(
    (c) => c.name === n || (c.aliases ?? []).includes(n),
  );
}

export interface CommandMatch {
  command: CommandDescriptor;
  /** Set when only an alias matched the query — the menu row says so. */
  alias?: string;
}

/**
 * Visible commands whose name or an alias contains `query`, in registry
 * order except that an exact name/alias match comes first — so "/new" + Enter
 * picks /new, not /new-leader.
 */
export function matchCommands(
  query: string,
  ctx: CommandContext,
  commands: CommandDescriptor[] = COMMANDS,
): CommandMatch[] {
  const q = query.toLowerCase();
  const matches: CommandMatch[] = [];
  for (const command of visible(commands, ctx)) {
    if (command.name.includes(q)) {
      matches.push({ command });
      continue;
    }
    const alias = (command.aliases ?? []).find((a) => a.includes(q));
    if (alias) matches.push({ command, alias });
  }
  const exact = matches.findIndex(
    (m) => m.command.name === q || (m.command.aliases ?? []).includes(q),
  );
  if (exact > 0) matches.unshift(...matches.splice(exact, 1));
  return matches;
}

/** "/task reproduce DNS error" → { name: "task", args: "reproduce DNS error" }; null unless the text starts with "/". */
export function parseCommandLine(
  text: string,
): { name: string; args: string } | null {
  const m = /^\/([^\s/]+)(?:\s+([\s\S]*))?$/.exec(text.trim());
  return m ? { name: m[1], args: (m[2] ?? "").trim() } : null;
}

/** The argument hint to show for `command` under `ctx`. */
export function argHintFor(
  command: CommandDescriptor,
  ctx: CommandContext,
): string | undefined {
  return typeof command.argHint === "function"
    ? command.argHint(ctx)
    : command.argHint;
}

export function availability(
  command: CommandDescriptor,
  ctx: CommandContext,
): CommandAvailability {
  return command.available?.(ctx) ?? { ok: true };
}
