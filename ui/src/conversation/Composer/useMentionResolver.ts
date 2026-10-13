import { useEffect, useState } from "react";
import {
  COMMANDS,
  argHintFor,
  availability,
  findCommand,
  matchCommands,
} from "./commands/registry";
import type { CommandContext, CommandDescriptor } from "./commands/types";

/** `@` references, `#` skills, `/` commands — three namespaces, three sources. */
export type TriggerChar = "@" | "#" | "/";

export interface MentionTrigger {
  type: TriggerChar;
  query: string;
  /** Index of the trigger character itself within the composer value. */
  start: number;
}

/**
 * Last unescaped `@`/`#`/`/` before the cursor — returns null when there's no
 * active trigger to resolve (HANDOFF.md §4). `@` mentions and `#` skills
 * never tolerate whitespace since the trigger char. `/` commands tolerate
 * exactly one interior space so a command that takes an argument (e.g.
 * "/agent claude") stays resolvable as a single trigger — the resolver splits
 * query on that space to drive the two-level command menu. A second space
 * still bails (that's just prose after a completed command, not a filter query).
 */
export function detectTrigger(
  value: string,
  cursor: number,
): MentionTrigger | null {
  let spaceSeen = false;
  for (let i = cursor - 1; i >= 0; i--) {
    const ch = value[i];
    if (ch === "@" || ch === "#" || ch === "/") {
      if (ch !== "/" && spaceSeen) return null;
      return { type: ch, query: value.slice(i + 1, cursor), start: i };
    }
    if (/\s/.test(ch)) {
      if (spaceSeen) return null;
      spaceSeen = true;
      continue;
    }
  }
  return null;
}

export type MentionCandidateKind =
  "mentionable" | "skill" | "command" | "command-option";

export interface MentionCandidate {
  id: string;
  label: string;
  detail?: string;
  /** Plain-text reference token spliced into the composer value on select. */
  insertText: string;
  kind: MentionCandidateKind;
  healthy?: boolean;
  /** "command" and "command-option" rows: the descriptor's name. */
  command?: string;
  /** "command" rows: the descriptor's argHint, shown after the name. */
  argHint?: string;
  /** "command" rows matched only through an alias: the alias that matched. */
  alias?: string;
  /** "command" rows whose available() said no — rendered disabled with this inline, never selectable. */
  disabledReason?: string;
  /** Set on level-2 items — the command name, drives the breadcrumb. */
  group?: string;
}

function commandCandidate(
  command: CommandDescriptor,
  alias: string | undefined,
  ctx: CommandContext,
): MentionCandidate {
  const avail = availability(command, ctx);
  return {
    id: `command:${command.name}`,
    label: `/${command.name}`,
    detail: command.description,
    insertText: `/${command.name}`,
    kind: "command",
    command: command.name,
    argHint: argHintFor(command, ctx),
    alias,
    disabledReason: avail.ok ? undefined : avail.reason,
  };
}

/** Level-1 "/" rows for `query` — synchronous, so a keypress can act on what's typed now. */
export function commandCandidates(
  query: string,
  ctx: CommandContext,
  commands: CommandDescriptor[] = COMMANDS,
): MentionCandidate[] {
  return matchCommands(query, ctx, commands).map((m) =>
    commandCandidate(m.command, m.alias, ctx),
  );
}

/** Same trigger (type, position and query) — whether candidates still describe what's typed. */
export function sameTrigger(
  a: MentionTrigger | null,
  b: MentionTrigger | null,
): boolean {
  return (
    a === b ||
    (!!a && !!b && a.type === b.type && a.start === b.start && a.query === b.query)
  );
}

/**
 * Watches the composer's cursor position for an active trigger, debounces
 * ~150ms, and resolves candidates: mentionables against the Router for `@`,
 * skill capabilities (capability-filtered, badged by `.healthy`) for `#`, and
 * the command registry (commands/registry.ts, names and aliases) for `/`
 * (level 1). Typing a space after a command with a submenu descends to level
 * 2 — that descriptor's options(), filtered by whatever follows the space.
 *
 * `trigger` follows the text at once; `candidates` lag it by the debounce.
 * `candidatesFor` is the trigger they were computed for — a keypress must
 * not act on rows resolved for a different query (see sameTrigger).
 */
export function useMentionResolver(
  ctx: CommandContext,
  value: string,
  cursor: number,
  debounceMs = 150,
  commands: CommandDescriptor[] = COMMANDS,
) {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null);
  const [resolved, setResolved] = useState<{
    candidates: MentionCandidate[];
    candidatesFor: MentionTrigger | null;
  }>({ candidates: [], candidatesFor: null });

  useEffect(() => {
    const active = detectTrigger(value, cursor);
    setTrigger(active);
    const setCandidates = (candidates: MentionCandidate[]) =>
      setResolved({ candidates, candidatesFor: active });
    if (!active) {
      setCandidates([]);
      return;
    }

    let cancelled = false;
    const { routerClient, agentId } = ctx;
    const handle = setTimeout(() => {
      void (async () => {
        if (active.type === "@") {
          const mentionables = await routerClient.getMentionables(active.query);
          if (cancelled) return;
          setCandidates(
            mentionables.map((m) => ({
              id: m.id,
              label: m.label,
              detail: m.detail,
              insertText: `@${m.label}`,
              kind: "mentionable" as const,
            })),
          );
        } else if (active.type === "#") {
          const capabilities = await routerClient.getSkillCapabilities(agentId);
          if (cancelled) return;
          const query = active.query.toLowerCase();
          setCandidates(
            capabilities
              .filter(
                (c) =>
                  c.type === "skill" && c.name.toLowerCase().includes(query),
              )
              .map((c) => ({
                id: c.id,
                label: `#${c.name}`,
                detail: c.description,
                insertText: `#${c.name}`,
                kind: "skill" as const,
                healthy: c.healthy,
              })),
          );
        } else {
          const spaceIdx = active.query.indexOf(" ");
          if (spaceIdx === -1) {
            setCandidates(commandCandidates(active.query, ctx, commands));
          } else {
            // Level 2 — head token picks the command, the rest filters its
            // options. An unknown head, a command without a submenu, or an
            // unavailable one resolves to no candidates (the line is then
            // either the command's args or plain text), not a crash.
            const command = findCommand(
              active.query.slice(0, spaceIdx),
              ctx,
              commands,
            );
            const argQuery = active.query.slice(spaceIdx + 1).toLowerCase();
            if (!command?.options || !availability(command, ctx).ok) {
              setCandidates([]);
              return;
            }
            const options = await command.options(ctx);
            if (cancelled) return;
            setCandidates(
              options
                .filter(
                  (o) => !argQuery || o.label.toLowerCase().includes(argQuery),
                )
                .map((o) => ({
                  id: o.id,
                  label: o.label,
                  detail: o.detail,
                  insertText: `/${command.name} ${o.label}`,
                  kind: "command-option" as const,
                  command: command.name,
                  group: command.name,
                })),
            );
          }
        }
      })();
    }, debounceMs);

    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
  }, [ctx, value, cursor, debounceMs, commands]);

  return { trigger, ...resolved };
}
