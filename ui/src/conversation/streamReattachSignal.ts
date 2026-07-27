// Cross-tree signal for "Restart agent" (SessionWorkspace's kebab, several
// layers above ConversationTab): a successful bounce needs to force a fresh
// /output connection immediately, without waiting on session.activity to
// flip and the passive streamBroken-reconnect effect to notice — the cached
// Session record can still read "inactive" right after the bounce (or the
// activity_changed event can lag), and a permanently silent transcript is
// indistinguishable from "nothing happened". Scoped by sessionId, same as
// SESSION_SHELL_REFETCH_EVENT_TYPES elsewhere, so a listener never reattaches
// on behalf of some other session.
export const STREAM_REATTACH_EVENT = "mads:reattach-stream";

export interface StreamReattachDetail {
  sessionId: string;
}

export function requestStreamReattach(sessionId: string) {
  window.dispatchEvent(
    new CustomEvent<StreamReattachDetail>(STREAM_REATTACH_EVENT, { detail: { sessionId } }),
  );
}
