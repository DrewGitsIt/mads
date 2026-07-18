/** Session-lifecycle SSE event types — shared by anything that needs to keep a `Session` live. */
export const SESSION_EVENT_TYPES = [
  "session.created",
  "session.renamed",
  "session.activity_changed",
  "session.updated",
  "session.deleted",
  "session.crashed",
];

/**
 * Every event type an open session workspace's shell (SessionWorkspace) and
 * its Conversation tab (ConversationTab) need between them: session
 * lifecycle, a linked-session inject/digest landing on the transcript, and
 * pipeline run status for the tab's run-dot. Both call sites must pass this
 * exact same array (not a fresh recombination) so ui/src/router/events.ts's
 * pooling — which dedupes by the exact resulting request URL — collapses
 * them into one browser connection instead of two or three: session-scoped
 * SSE connections are already close to the HTTP/1.1 6-per-origin socket
 * cap once the rail's global subscriptions and this session's /output
 * stream are counted too, and each extra one here starves other fetches
 * (e.g. GET /transcript) queued behind it. A handler that doesn't care
 * about a given type in this list just ignores it.
 */
export const WORKSPACE_SESSION_EVENT_TYPES = [
  ...SESSION_EVENT_TYPES,
  "link.message_injected",
  "pipeline.started",
  "pipeline.completed",
  "pipeline.cancelled",
  "pipeline.failed",
];
