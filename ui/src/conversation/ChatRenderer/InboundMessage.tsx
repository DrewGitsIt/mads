import { useState } from "react";
import type { InboundSource } from "../../router/types";
import { HarnessMark, harnessLabel } from "../../features/sessions/rail/HarnessMark";
import { Markdown } from "./markdown";
import { formatMessageTime, parseMessageTime } from "./messageTime";
import styles from "./InboundMessage.module.css";

/** Bodies longer than this fold behind "Show more" (the full text stays in the DOM, so find still reaches it). */
const FOLD_CHARS = 480;
const FOLD_LINES = 8;

/**
 * A message that reached the agent from outside MADS — today another agent
 * session, sent with its harness's own tool. It is the notification family
 * (a system row, never the human's voice) with its text readable, and
 * visibly not a link-delivered inject: it did not travel through a MADS
 * link, so it wears the receiving harness's mark and a dashed edge instead
 * of the link chip. A message the harness held back keeps the row but goes
 * quiet and says why.
 *
 * `relation` is how a resolved link relates the sender to this session
 * ("your leader"); the caller works that out from the links it holds.
 */
export function InboundMessage({
  text,
  inbound,
  timestamp,
  relation,
  findKey,
}: {
  text?: string;
  inbound: InboundSource;
  timestamp?: string;
  relation?: string;
  findKey?: string;
}) {
  const [open, setOpen] = useState(false);
  const at = parseMessageTime(timestamp);
  const harness = harnessLabel(inbound.provider);
  const sender = inbound.session_name ?? inbound.from_name ?? "another session";
  const held = inbound.state === "held";
  const body = text?.trim() ?? "";
  const long = body.length > FOLD_CHARS || body.split("\n").length > FOLD_LINES;
  const folded = long && !open;
  return (
    <div className={`${styles.inbound} ${held ? styles.held : ""}`} role="note" data-find={findKey}>
      <div className={styles.header}>
        <HarnessMark provider={inbound.provider} />
        {held ? (
          <span>
            Message from <b>{sender}</b> held by {harness}
            {inbound.hold_reason ? `: ${inbound.hold_reason}` : ""}
          </span>
        ) : (
          <>
            <span>
              from <b>{sender}</b>
            </span>
            {relation && <span className={styles.hint}>· {relation}</span>}
            <span className={styles.hint}>· via {harness}</span>
          </>
        )}
        {at && (
          <time className={styles.time} dateTime={at.toISOString()} title={at.toLocaleString()}>
            {formatMessageTime(at)}
          </time>
        )}
      </div>
      {body && (
        <>
          <div className={folded ? styles.folded : undefined}>
            <Markdown className={styles.prose} text={body} />
          </div>
          {long && (
            <button type="button" className={styles.disclose} aria-expanded={open} onClick={() => setOpen(!open)}>
              {open ? "Show less" : "Show more"}
            </button>
          )}
        </>
      )}
    </div>
  );
}
