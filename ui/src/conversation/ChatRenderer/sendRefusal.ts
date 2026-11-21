import { SessionWakeError } from "../../router/client";

/**
 * A send the session's host refused for this device's grant
 * (docs/MESH-AUTH-DESIGN.md D3/D4): the host's Mesh Service answers 403 with
 * a code, relayed unchanged by this device's Router, which sendMessage
 * throws as a SessionWakeError carrying it.
 *   - full_trust_chat_refused: C1, this device may chat but not with
 *     Full access sessions there.
 *   - held_unsupported: C1 "held", refused until hosts can hold messages.
 *   - no_access: this device may only view (or nothing) there.
 */
export type RefusalKind = "c1" | "view" | "none";

export interface RefusedSend {
  text: string;
  kind: RefusalKind;
  /** The host's own words, for Why. */
  message: string;
}

export function refusalOf(err: unknown, text: string): RefusedSend | null {
  if (!(err instanceof SessionWakeError)) return null;
  switch (err.code) {
    case "full_trust_chat_refused":
    case "held_unsupported":
      return { text, kind: "c1", message: err.message };
    case "no_access":
      // "… needs "chat" here; this device has "view"" vs "… grants device … nothing".
      return { text, kind: /has "view"/.test(err.message) ? "view" : "none", message: err.message };
    default:
      return null;
  }
}
