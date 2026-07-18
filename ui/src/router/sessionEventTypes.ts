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
 * Subset of WORKSPACE_SESSION_EVENT_TYPES that means "the Session object
 * itself may have changed" — SessionWorkspace's own dispatcher refetches on
 * exactly these, explicitly, rather than an else-catch-all, since the array
 * below also carries pipeline-run-fold-only types (task.x, stage.x, etc.) that
 * a Session refetch has no reason to react to.
 */
export const SESSION_SHELL_REFETCH_EVENT_TYPES = [
  ...SESSION_EVENT_TYPES,
  "link.message_injected",
  "merge.attempted",
  "merge.conflicted",
  "merge.completed",
];

/**
 * Every event type an open session workspace's shell (SessionWorkspace), its
 * Conversation tab (ConversationTab), its Files tab's merge-back flow
 * (MergeBackFlow), and its Pipeline tab's live run fold (usePipelineRun)
 * need between them: session lifecycle, a linked-session inject/digest
 * landing on the transcript, pipeline run status for the tab's run-dot,
 * merge-back attempt/conflict/completion status, and the Pipeline tab's own
 * task/stage/file fold. All call sites must pass this exact same array (not
 * a fresh recombination) so ui/src/router/events.ts's pooling — which
 * dedupes by the exact resulting request URL — collapses them into one
 * browser connection instead of several: session-scoped SSE connections are
 * already close to the HTTP/1.1 6-per-origin socket cap once the rail's
 * global subscriptions and this session's /output stream are counted too,
 * and each extra one here starves other fetches (e.g. GET /transcript)
 * queued behind it. A handler that doesn't care about a given type in this
 * list just ignores it.
 */
export const WORKSPACE_SESSION_EVENT_TYPES = [
  ...SESSION_SHELL_REFETCH_EVENT_TYPES,
  "pipeline.started",
  "pipeline.completed",
  "pipeline.cancelled",
  "pipeline.failed",
  "task.created",
  "task.updated",
  "task.status_changed",
  "note.created",
  "stage.started",
  "stage.completed",
  "stage.failed",
  "stage.skipped",
  "file.changed",
];
