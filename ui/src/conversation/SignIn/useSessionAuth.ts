import { useCallback, useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { SessionAuth } from "../../router/types";

const AUTH_EVENT_TYPES = ["account.auth_changed"];

/**
 * The login a session runs on and whether it is signed out
 * (GET /sessions/{id}/auth, docs/AUTH-SIGNIN-DESIGN.md), kept live by
 * `account.auth_changed`. Signing in from any session flips every session on
 * that login, so any change refetches. Undefined until it answers, and on any
 * failure (a Router without the route answers 404): the conversation then
 * shows failed sends exactly as it did before sign-in existed.
 */
export function useSessionAuth(
  routerClient: RouterClient,
  sessionId: string,
): { auth: SessionAuth | undefined; refetch: () => void } {
  const [auth, setAuth] = useState<SessionAuth>();
  const [token, setToken] = useState(0);
  const refetch = useCallback(() => setToken((t) => t + 1), []);

  useEffect(() => {
    setAuth(undefined);
  }, [routerClient, sessionId]);

  useEffect(() => {
    let cancelled = false;
    function load() {
      routerClient.getSessionAuth(sessionId).then(
        (next) => !cancelled && setAuth(next),
        () => !cancelled && setAuth(undefined),
      );
    }
    load();
    const unsubscribe = subscribeToEvents({ types: AUTH_EVENT_TYPES }, load);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId, token]);

  return { auth, refetch };
}
