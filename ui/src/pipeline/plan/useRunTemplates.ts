import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { Template } from "../../router/types";
import { useSessionWorkspace } from "../../tasks/useSessionWorkspace";

/** The template a run uses when the session's workspace doesn't bind one. */
export const DEFAULT_TEMPLATE_ID = "default-implementation";

const TEMPLATE_EVENT_TYPES = ["template.created", "template.updated", "template.deleted"];

export interface UseRunTemplates {
  templates: Template[];
  /**
   * The workspace's bound template (`defaults.template`, which the workspace
   * editor stores by name; an id is accepted too), resolved to its id. Falls
   * back to DEFAULT_TEMPLATE_ID when none is bound or it names no template.
   * Can change once the workspace resolves, so seed a picker from it rather
   * than snapshotting it on first render.
   */
  defaultTemplateId: string;
  loading: boolean;
  error: string | null;
}

/** The templates a Plan can run, and which one it starts on. Kept live via template.* events. */
export function useRunTemplates(routerClient: RouterClient, sessionId: string): UseRunTemplates {
  const workspace = useSessionWorkspace(routerClient, sessionId);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const list = await routerClient.listTemplates();
        if (!cancelled) setTemplates(list);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void load();
    const unsubscribe = subscribeToEvents({ types: TEMPLATE_EVENT_TYPES }, () => {
      void load();
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient]);

  const bound = workspace?.defaults.template;
  const defaultTemplateId = useMemo(() => {
    if (!bound) return DEFAULT_TEMPLATE_ID;
    const match = templates.find((t) => t.id === bound) ?? templates.find((t) => t.name === bound);
    return match?.id ?? DEFAULT_TEMPLATE_ID;
  }, [bound, templates]);

  return { templates, defaultTemplateId, loading, error };
}
