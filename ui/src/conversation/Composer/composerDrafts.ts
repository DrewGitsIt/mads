// Per-session composer drafts, held in memory for the lifetime of the app
// session. The Composer unmounts when you switch sessions or go back to the
// launchpad (SessionWorkspace is keyed by session id), so without this an
// in-progress message is lost on navigation. Cleared implicitly when the
// composer value goes empty (a successful send clears the textarea).
const drafts = new Map<string, string>();

export function getDraft(sessionId: string): string {
  return drafts.get(sessionId) ?? "";
}

export function setDraft(sessionId: string, value: string): void {
  if (value) drafts.set(sessionId, value);
  else drafts.delete(sessionId);
}

/** Test-only: reset the in-memory store so module state doesn't leak between cases. */
export function clearAllDrafts(): void {
  drafts.clear();
}
