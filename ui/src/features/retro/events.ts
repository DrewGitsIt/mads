// Job-lifecycle events for one in-flight generation run. Concrete literal
// strings only — see ui/src/router/events.ts: subscribeToEvents attaches
// addEventListener per literal in this list; "retro.*" would filter
// correctly server-side and receive nothing client-side.
export const RETRO_GENERATION_EVENT_TYPES = [
  "retro.started",
  "retro.progress",
  "retro.completed",
  "retro.failed",
  "retro.cancelled",
];

// Refetch triggers for the history list and any open detail view.
export const RETRO_LIST_EVENT_TYPES = [
  "retro.completed",
  "retro.dpa_accepted",
  "retro.dpa_rejected",
  "retro.dpa_edited",
  "retro.follow_up_created",
];
