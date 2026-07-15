import { ROUTER_BASE } from "./client";
import type { Envelope } from "./types";

export interface SubscribeParams {
  since?: number;
  sessionId?: string;
  types?: string[];
}

/**
 * Subscribes to the Router's /events SSE stream. Returns an unsubscribe
 * function that closes the underlying EventSource.
 *
 * The Router always names each frame's `event:` field after the envelope's
 * type (e.g. "session.created"), never a bare "message" — so callers must
 * pass `types` to receive anything; it both narrows the server-side stream
 * and tells this wrapper which named listeners to attach.
 */
export function subscribeToEvents(
  params: SubscribeParams,
  onEvent: (envelope: Envelope) => void,
): () => void {
  const url = new URL("/events", ROUTER_BASE);
  if (params.since !== undefined) url.searchParams.set("since", String(params.since));
  if (params.sessionId) url.searchParams.set("session_id", params.sessionId);
  if (params.types && params.types.length > 0) {
    url.searchParams.set("types", params.types.join(","));
  }

  const source = new EventSource(url);

  const handleMessage = (e: MessageEvent<string>) => {
    onEvent(JSON.parse(e.data) as Envelope);
  };

  for (const type of params.types ?? []) {
    source.addEventListener(type, handleMessage as EventListener);
  }

  return () => {
    source.close();
  };
}
