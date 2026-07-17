/** Session-lifecycle SSE event types — shared by anything that needs to keep a `Session` live. */
export const SESSION_EVENT_TYPES = [
  "session.created",
  "session.renamed",
  "session.activity_changed",
  "session.updated",
  "session.deleted",
  "session.crashed",
];
