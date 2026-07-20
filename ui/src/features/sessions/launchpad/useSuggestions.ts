import { useEffect, useState } from "react";
import { router } from "../../../router/client";

export interface Suggestion {
  id: string;
  label: string;
  sessionId: string;
  kind: "resume" | "pickup";
  /** Set for "pickup" suggestions — the ready task the session should be opened to. */
  taskId?: string;
}

/**
 * Plain Data-Service-backed queries, no LLM (MADS-UX §3.2): sessions with
 * open tasks ("attention") plus ready tasks. `GET /sessions?attention=true`
 * isn't a real Data Service filter (confirmed against
 * services/data/internal/api/sessions.go), so "attention" is derived
 * client-side from GET /sessions + GET /tasks rather than a server query.
 */
export function useSuggestions(): Suggestion[] {
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      const [sessions, readyTasks] = await Promise.all([
        router.listSessions(),
        router.listAllTasks("ready"),
      ]);
      if (cancelled) return;

      const openCountBySession = new Map<string, number>();
      for (const task of readyTasks) {
        openCountBySession.set(task.session_id, (openCountBySession.get(task.session_id) ?? 0) + 1);
      }

      const attentionSessions = sessions.filter((s) => openCountBySession.has(s.id));
      const next: Suggestion[] = [];
      if (attentionSessions.length > 0) {
        const s = attentionSessions[0];
        const n = openCountBySession.get(s.id) ?? 0;
        next.push({
          id: `resume-${s.id}`,
          kind: "resume",
          sessionId: s.id,
          label: `Resume: ${s.name} — ${n} open task${n === 1 ? "" : "s"}`,
        });
      }
      const readyTask = readyTasks[0];
      if (readyTask) {
        next.push({
          id: `pickup-${readyTask.id}`,
          kind: "pickup",
          sessionId: readyTask.session_id,
          taskId: readyTask.id,
          label: `Pick up: ${readyTask.title} (${readyTask.id})`,
        });
      }
      setSuggestions(next.slice(0, 3));
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, []);

  return suggestions;
}
