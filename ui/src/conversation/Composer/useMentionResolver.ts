import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";

export interface MentionTrigger {
  type: "@" | "/";
  query: string;
  /** Index of the trigger character itself within the composer value. */
  start: number;
}

/**
 * Last unescaped `@`/`/` before the cursor with no whitespace since —
 * returns null when there's no active trigger to resolve (HANDOFF.md §4).
 */
export function detectTrigger(value: string, cursor: number): MentionTrigger | null {
  for (let i = cursor - 1; i >= 0; i--) {
    const ch = value[i];
    if (ch === "@" || ch === "/") {
      return { type: ch, query: value.slice(i + 1, cursor), start: i };
    }
    if (/\s/.test(ch)) return null;
  }
  return null;
}

export type MentionCandidateKind = "mentionable" | "command";

export interface MentionCandidate {
  id: string;
  label: string;
  detail?: string;
  /** Plain-text reference token spliced into the composer value on select. */
  insertText: string;
  kind: MentionCandidateKind;
  healthy?: boolean;
}

const STATIC_COMMANDS = ["task", "model", "agent"];

/**
 * Watches the composer's cursor position for an active `@`/`/` trigger,
 * debounces ~150ms, and resolves candidates against the Router: mentionables
 * for `@`, skill capabilities (capability-filtered, badged by `.healthy`)
 * plus the static mads command list for `/`.
 */
export function useMentionResolver(
  routerClient: RouterClient,
  agentId: string | undefined,
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
            kind: "command" as const,
          }));
          const all = [...staticCandidates, ...skillCandidates];
          setCandidates(
            query ? all.filter((c) => c.label.toLowerCase().includes(query)) : all,
          );
        }
      })();
    }, debounceMs);

    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
  }, [routerClient, agentId, value, cursor, debounceMs]);

  return { trigger, candidates };
}
