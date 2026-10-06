import { useEffect, useRef } from "react";
import { subscribeToEvents } from "../../router/events";
import { useLiveShells } from "./useLiveShells";

/**
 * D7: when the control plane reports `session.deleted`, the session's shell
 * (if it has a live one) is ended — nothing else would ever end it, and the
 * delete dialog already said it would. Mounted once, in App, rather than in
 * the rail or a workspace: those unmount when you leave the Sessions surface
 * or deselect the session, and a delete from the CLI or another window can
 * land at any time. Outside Electron there is no shell host and nothing to do.
 *
 * It subscribes to the shared event bus (a type already in the session.*
 * domain, so no extra connection) and asks main's shell cache — through
 * useLiveShells — whether the session has a shell before it touches the host,
 * so a session that never opened one doesn't make the host fork.
 */
export function useEndDeletedSessionShells(): void {
  const shells = useLiveShells();
  const shellsRef = useRef(shells);
  shellsRef.current = shells;

  useEffect(() => {
    const host = typeof window !== "undefined" ? window.mads?.shell : undefined;
    if (!host) return;
    return subscribeToEvents({ types: ["session.deleted"] }, (envelope) => {
      const sessionId = envelope.session_id;
      if (!sessionId || !shellsRef.current.some((s) => s.sessionId === sessionId)) return;
      host.endSession(sessionId).catch(() => undefined);
    });
  }, []);
}
