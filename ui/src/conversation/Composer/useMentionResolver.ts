import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";

export interface MentionTrigger {
  type: "@" | "/";
  query: string;
  /** Index of the trigger character itself within the composer value. */
  start: number;
}

/**
 * Last unescaped `@`/`/` before the cursor — returns null when there's no
 * active trigger to resolve (HANDOFF.md §4). `@` mentions never tolerate
 * whitespace since the trigger char. `/` commands tolerate exactly one
 * interior space so a command that takes an argument (e.g. "/agent claude")
 * stays resolvable as a single trigger — the resolver splits query on that
 * space to drive the two-level command menu. A second space still bails
 * (that's just prose after a completed command, not a filter query).
 */
export function detectTrigger(value: string, cursor: number): MentionTrigger | null {
  let spaceSeen = false;
  for (let i = cursor - 1; i >= 0; i--) {
    const ch = value[i];
    if (ch === "@" || ch === "/") {
      if (ch === "@" && spaceSeen) return null;
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

export type MentionCandidateKind = "mentionable" | "command" | "command-group" | "command-action";

export interface MentionCandidate {
  id: string;
  label: string;
  detail?: string;
  /** Plain-text reference token spliced into the composer value on select. */
  insertText: string;
  kind: MentionCandidateKind;
  healthy?: boolean;
  /** Set on level-2 items ("agent"/"model") — drives the breadcrumb and dispatch target. */
  group?: "agent" | "model";
}

const STATIC_COMMANDS = ["task", "model", "agent"];
// Commands that take an argument: selecting one advances the menu to a
// second level instead of inserting text/executing immediately.
const GROUP_COMMANDS = new Set(["agent", "model"]);

/**
 * Watches the composer's cursor position for an active `@`/`/` trigger,
 * debounces ~150ms, and resolves candidates against the Router: mentionables
 * for `@`, skill capabilities (capability-filtered, badged by `.healthy`)
 * plus the static mads command list for `/` (level 1). Typing a space after
 * an argument-taking command ("agent"/"model") descends to level 2 —
 * registry agents or the current provider's model list, filtered by
 * whatever follows the space.
 */
export function useMentionResolver(
  routerClient: RouterClient,
  agentId: string | undefined,
  provider: string | undefined,
  value: string,
  cursor: number,
  debounceMs = 150,
) {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null);
  const [candidates, setCandidates] = useState<MentionCandidate[]>([]);

  useEffect(() => {
    const active = detectTrigger(value, cursor);
    setTrigger(active);
    if (!active) {
      setCandidates([]);
      return;
    }

    let cancelled = false;
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
        } else {
          const spaceIdx = active.query.indexOf(" ");
          if (spaceIdx === -1) {
            const capabilities = await routerClient.getSkillCapabilities(agentId);
            if (cancelled) return;
            const query = active.query.toLowerCase();
            const skillCandidates: MentionCandidate[] = capabilities
              .filter((c) => c.type === "skill")
              .map((c) => ({
                id: c.id,
                label: `/${c.name}`,
                detail: c.description,
                insertText: `/${c.name}`,
                kind: "command" as const,
                healthy: c.healthy,
              }));
            const staticCandidates: MentionCandidate[] = STATIC_COMMANDS.map((name) => ({
              id: `static:${name}`,
              label: `/${name}`,
              insertText: `/${name}`,
              kind: GROUP_COMMANDS.has(name) ? ("command-group" as const) : ("command" as const),
            }));
            const all = [...staticCandidates, ...skillCandidates];
            setCandidates(
              query ? all.filter((c) => c.label.toLowerCase().includes(query)) : all,
            );
          } else {
            // Level 2 — head token picks the group, the rest filters it.
            // An unrecognized head (typo, or a no-argument command followed
            // by stray text) just resolves to no candidates, not a crash.
            const head = active.query.slice(0, spaceIdx).toLowerCase();
            const argQuery = active.query.slice(spaceIdx + 1).toLowerCase();
            if (head === "agent") {
              const agents = await routerClient.listAgents();
              if (cancelled) return;
              const items: MentionCandidate[] = agents.map((a) => ({
                id: a.id,
                label: a.name,
                detail: a.description,
                insertText: `/agent ${a.name}`,
                kind: "command-action" as const,
                group: "agent" as const,
              }));
              setCandidates(
                argQuery ? items.filter((c) => c.label.toLowerCase().includes(argQuery)) : items,
              );
            } else if (head === "model" && provider) {
              const models = await routerClient.listModels(provider);
              if (cancelled) return;
              const items: MentionCandidate[] = models.map((m) => ({
                id: m.id,
                label: m.name,
                insertText: `/model ${m.name}`,
                kind: "command-action" as const,
                group: "model" as const,
              }));
              setCandidates(
                argQuery ? items.filter((c) => c.label.toLowerCase().includes(argQuery)) : items,
              );
            } else {
              setCandidates([]);
            }
          }
        }
      })();
    }, debounceMs);

    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
  }, [routerClient, agentId, provider, value, cursor, debounceMs]);

  return { trigger, candidates };
}
