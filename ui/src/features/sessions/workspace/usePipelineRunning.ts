/** Pipeline run-status event types — folded into SessionWorkspace's single session-scoped subscription (see its comment) rather than a dedicated one, to stay under the browser's per-origin connection cap. */
export const RUN_EVENT_TYPES = ["pipeline.started", "pipeline.completed", "pipeline.cancelled", "pipeline.failed"];
