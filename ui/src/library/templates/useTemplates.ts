import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { eventsReady, subscribeToEvents } from "../../router/events";
import type { Template } from "../../router/types";

const TEMPLATE_EVENT_TYPES = ["template.created", "template.updated", "template.deleted"];

export interface UseTemplates {
  templates: Template[];
  loading: boolean;
  error: string | null;
  createTemplate: (template: Partial<Template>) => Promise<Template>;
  updateTemplate: (template: Template) => Promise<Template>;
  deleteTemplate: (id: string) => Promise<void>;
}

/** Owns the shared template list — fetched once, kept live via the template.* SSE domain. */
export function useTemplates(): UseTemplates {
  const [templates, setTemplates] = useState<Template[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listTemplates();
      if (mounted.current) setTemplates(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    let live = true;
    const unsubscribe = subscribeToEvents({ types: TEMPLATE_EVENT_TYPES }, () => {
      void refetch();
    });
    // The mount read waits for the bus's ready barrier (router/events.ts
    // eventsReady), subscribed first: the bus never replays, so a read served
    // before it fixes its cursor could miss an event landing in between.
    void eventsReady().then(() => {
      if (live) void refetch();
    });
    return () => {
      live = false;
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  const createTemplate = useCallback(async (template: Partial<Template>) => {
    return router.createTemplate(template as Template);
  }, []);

  const updateTemplate = useCallback(async (template: Template) => {
    return router.updateTemplate(template);
  }, []);

  const deleteTemplate = useCallback(async (id: string) => {
    await router.deleteTemplate(id);
  }, []);

  return { templates, loading, error, createTemplate, updateTemplate, deleteTemplate };
}
