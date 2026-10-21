import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Chip } from "../../components/Chip/Chip";
import { FOLD_MS, Fold, useArrivals } from "../../components/Motion";
import { FLAGS } from "../../featureFlags";
import type {
  Attribution,
  AttributionKind,
  BackgroundTask,
  Link,
  PermissionRequest,
  ToolStatus,
  ToolUse,
  TranscriptMessage,
} from "../../router/types";
import { ActivityGroup } from "../activity/ActivityGroup";
import type { LinkNameResolver } from "../activity/toolDisplay";
import type { PermissionReveal } from "../ApprovalJump/permissionReveal";
import {
  anchorPermissions,
  buildItems,
  interleaveCards,
  rowsToEntries,
  type TaskModel,
} from "../activity/conversationItems";
import type { KeyedRow } from "../keyed/conversation";
import { joinRows } from "../keyed/joinRule";
import { PlanWidget } from "../activity/PlanWidget";
import { findAll, findPattern, searchEntries, type EntryMatch, type SearchCache } from "../Find/findText";
import { useFind, type FindRequest, type FindStatus } from "../Find/useFind";
import { WorktreeCard, type WorktreeCardActions } from "../Worktree/WorktreeCard";
import type { WorktreeCardModel } from "../Worktree/worktreeCards";
import { TaskCard } from "../activity/TaskCard";
import { Markdown } from "./markdown";
import { CompactionMarker } from "./CompactionMarker";
import { collapseFailedSends } from "./failedSends";
import { formatMessageTime, parseMessageTime } from "./messageTime";
import { InboundMessage } from "./InboundMessage";
import { MessageAttachments } from "./MessageAttachments";
import { PermissionCard } from "./PermissionCard";
import { SendDelivery } from "./SendDelivery";
import { splitAttachments } from "../attachmentRefs";
import styles from "./ChatRenderer.module.css";

export interface ChatRendererProps {
  /**
   * The keyed conversation's main list (useKeyedConversation), in order and
   * as revealed (useReveal): stored and live rows alike, a tool's result on
   * its call's row, subagent rows left out.
   */
  rows: KeyedRow[];
  /** Linked sessions (flag-gated): links touching the viewed session — only for a digest card's cadence line, matched by sender name (an attribution parsed from the message wrapper carries no link id). */
  links?: Link[];
  viewedSessionId?: string;
  /** Names a link's other session (useLinkNames), to match a digest's sender to its link. Absent → no cadence line. */
  linkName?: LinkNameResolver;
  /**
   * Where the Router serves the viewed session's attachments — lets a user
   * message's attached files show as thumbnails. Absent → names only.
   */
  attachmentUrl?: (attachmentId: string, variant?: "thumbnail") => string;
  /**
   * A turn is in flight, or its reply is still revealing — the current
   * turn's activity group renders open and grows in place, collapsing once
   * this goes false.
   */
  turnActive?: boolean;
  /** Live text is paced (lands a code block whole): a code block arriving in it folds in. */
  foldNewCode?: boolean;
  /** Out-of-band status changes (session.tool_status_changed + replay), by tool_use id. */
  toolStatuses?: ReadonlyMap<string, ToolStatus>;
  /** Calls still running as background processes, by tool_use id (the live "background" frame). */
  background?: ReadonlyMap<string, BackgroundTask>;
  /** Task subagent lifecycles keyed by the spawning tool_use id — drives Task cards. */
  tasks?: ReadonlyMap<string, TaskModel>;
  /** Tool whose detail is open in the dock — highlights its row and suppresses its peek. */
  selectedToolId?: string;
  /** Row click → open/retarget the bottom dock on this tool. */
  onOpenTool?: (tool: ToolUse) => void;
  /** Task card click → open the subagent's stream in the dock (keyed by spawning tool_use id). */
  onOpenTask?: (toolUseId: string, tool: ToolUse) => void;
  /** Commit/Land result cards (event-log built), interleaved by time. */
  worktreeCards?: WorktreeCardModel[];
  worktreeActions?: WorktreeCardActions;
  /**
   * Permission cards: each renders under its call's row (or its subagent's
   * task card); the rest trail the stream (see anchorPermissions).
   */
  permissionRequests?: PermissionRequest[];
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
  /** The approval jump's latest reveal — a collapsed group holding that card opens. */
  permissionReveal?: PermissionReveal | null;
  /**
   * Resend a message that failed. Offered only on the latest user message,
   * and only between turns; the resend collapses into it (see
   * collapseFailedSends).
   */
  onRetry?: (text: string) => void;
  /** Why the latest send failed (its HTTP error, or the stream's error frame), for a send the stream reported unsaved. */
  sendFailure?: string;
  /** Find in conversation (useFind): absent or an empty query is off. */
  find?: FindRequest;
  onFindStatus?: (status: FindStatus) => void;
}

const noResolve = async () => {};

const CADENCE_LABEL: Record<Link["cadence"], string> = {
  per_turn: "cadence: digest after each burst of activity",
  on_signal: "cadence: wakes only when the session needs attention",
  manual_only: "cadence: manual — no automatic digests",
};

/** A digest push the monitor answered with exactly NOOP (the framing every
 * monitor-facing push carries) collapses to a quiet-check chip; consecutive
 * quiet checks aggregate into one, keeping the digests it absorbed so the
 * chip can expand to show them. */
type QuietItem = { key: string; type: "quiet"; digests: QuietDigest[] };
type QuietDigest = {
  key: string;
  source: string;
  kind: "digest" | "signal";
  time?: Date;
  text: string;
  cadence?: Link["cadence"];
};

/** A finished activity group of only thinking rows — what a thinking model
 * (claude-code thinks by default) leaves between a digest and its NOOP. A
 * group with any tool call means the monitor did real work, so it is not
 * skippable. */
function isThinkingOnly(item: ReturnType<typeof buildItems>[number] | undefined): boolean {
  return (
    item?.type === "activity" &&
    !item.live &&
    item.entries.length > 0 &&
    item.entries.every((e) => e.type === "thinking")
  );
}

function DigestCard({ findKey, source, kind = "digest", time, text, cadence }: {
  findKey?: string;
  source: string;
  kind?: QuietDigest["kind"];
  time?: Date;
  text: string;
  cadence?: Link["cadence"];
}) {
  return (
    <div className={styles.digest} data-find={findKey}>
      <div className={styles.digestHeader}>
        ⌗ {kind} · <b>{source}</b>
        {time && ` · ${formatMessageTime(time)}`}
      </div>
      <Markdown className={styles.prose} text={text} />
      {cadence && <div className={styles.digestCadence}>{CADENCE_LABEL[cadence]}</div>}
    </div>
  );
}

/** The sessions behind a pill's updates, in order of first appearance, with
 * how many updates each sent. */
function quietSessions(digests: QuietDigest[]): { name: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const d of digests) counts.set(d.source, (counts.get(d.source) ?? 0) + 1);
  return [...counts].map(([name, count]) => ({ name, count }));
}

/** What the pill says it is: "4 updates from Pipeline Design Study · no
 * action needed" — the session names for one or two sources, a count past that. */
export function quietLabel(digests: QuietDigest[]): string {
  const names = quietSessions(digests).map((s) => s.name || "a linked session");
  const n = digests.length;
  const from =
    names.length === 1 ? names[0] : names.length === 2 ? `${names[0]} and ${names[1]}` : `${names.length} sessions`;
  return `${n} ${n === 1 ? "update" : "updates"} from ${from} · no action needed`;
}

/** The pill for digests and signals the agent answered NOOP; clicking it
 * reveals them, grouped by session. `ease` is the height (px) of what just
 * folded into it, shrunk to nothing so the conversation below glides up
 * instead of jumping. */
function QuietChecks({ item, ease }: { item: QuietItem; ease?: number }) {
  const [open, setOpen] = useState(false);
  const sessions = quietSessions(item.digests);
  // Oldest first within a session: the transcript order, grouped.
  const grouped = sessions.flatMap((s) => item.digests.filter((d) => d.source === s.name));
  return (
    <div
      className={styles.quietGroup}
      data-ease={ease ? true : undefined}
      style={ease ? ({ "--ease-from": `${ease}px` } as CSSProperties) : undefined}
    >
      <div className={styles.quiet}>
        <Chip tone="neutral" pressed={open} onClick={() => setOpen((o) => !o)}>
          {open ? "▾" : "▸"} {quietLabel(item.digests)}
        </Chip>
      </div>
      {open && (
        <>
          <div className={styles.quietSessions}>
            {sessions.map((s) => `${s.name} ×${s.count}`).join(" · ")}
          </div>
          {grouped.map((d) => (
            <DigestCard key={d.key} source={d.source} kind={d.kind} time={d.time} text={d.text} cadence={d.cadence} />
          ))}
        </>
      )}
    </div>
  );
}

/** A short title for a notice, so its collapsed row says what it is. The
 * grant notice (router: orchestrateGrantNotice) is the one worth naming. */
export function noticeTitle(a: Attribution, text: string): string {
  if (/^The user has linked you to the session\b[^]*\bas its leader\b/.test(text)) {
    return "Leader tools provided to this session";
  }
  return `Notice from ${a.source_name || "mads"}`;
}

/** A notice from mads: collapsed to a self-explanatory line, expandable to
 * the message; "acknowledged" when the agent answered it NOOP. */
function NoticeRow({ findKey, attribution, text, acknowledged }: {
  findKey: string;
  attribution: Attribution;
  text: string;
  acknowledged: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={styles.injected} data-find={findKey}>
      <span className={styles.noticeHead}>
        <Chip tone="info" pressed={open} onClick={() => setOpen((o) => !o)}>
          {open ? "▾" : "▸"} [System] {noticeTitle(attribution, text)}
          {!open && " · expand to show message"}
        </Chip>
        {acknowledged && (
          <span className={styles.noticeAck} title="The agent answered NOOP">
            acknowledged
          </span>
        )}
      </span>
      <Fold open={open}>
        <Markdown className={styles.prose} text={text} />
      </Fold>
    </div>
  );
}

/** What an attributed message is, from the kind its wrapper names
 * (docs/LINKED-SESSIONS-DESIGN.md, "Message wrapper"). Absent reads as a
 * message, the delivery default. */
function kindOf(a: Attribution): AttributionKind {
  return a.kind ?? "message";
}

/** Would this message render as a digest card? A digest or signal only
 * ever reaches the watching side, so the viewer is the recipient by
 * construction — no link lookup decides it. */
function isDigestCard(message: TranscriptMessage): boolean {
  if (!message.attribution || !FLAGS.linkedSessions) return false;
  const kind = kindOf(message.attribution);
  return kind === "digest" || kind === "signal";
}

/** A notice from mads itself (a grant notice, a pipeline's folder notice):
 * never the user's voice, and not gated on linking. */
function isNotice(message: TranscriptMessage): boolean {
  return Boolean(message.attribution && kindOf(message.attribution) === "notice");
}

/** The link a digest card came over, for its cadence line: the viewed
 * session's watching link whose other side is named `from`, or its implicit
 * hand-raiser for "your pipeline run". Undefined when it can't be told. */
function digestLink(
  a: Attribution,
  links: Link[],
  viewedSessionId: string | undefined,
  linkName: LinkNameResolver | undefined,
): Link | undefined {
  const watching = links.filter((l) => l.granting_session_id === viewedSessionId);
  if (a.relation === "your pipeline run") return watching.find((l) => l.implicit);
  if (!linkName) return undefined;
  const named = watching.filter((l) => !l.implicit && linkName(l.id) === a.source_name);
  return named.length === 1 ? named[0] : undefined;
}

/** A finished agent reply of exactly NOOP at items[j], past any
 * thinking-only activity from items[from]: the index of the reply, or -1. */
function noopReplyAt(items: ReturnType<typeof buildItems>, from: number): number {
  let j = from;
  while (isThinkingOnly(items[j])) j++;
  const reply = items[j];
  return reply?.type === "message" &&
    !reply.live &&
    reply.message.role === "agent" &&
    (reply.message.text ?? "").trim() === "NOOP"
    ? j
    : -1;
}

/**
 * Maps a transcript to prose + always-grouped activity (T12): consecutive
 * tool/thinking events render as one collapsible group of rows, TodoWrite
 * becomes the in-place-updating plan widget (T14), and Agent spawns become
 * Task subagent cards (T15). Live and stored rows render through the same
 * components, keyed by row key — the only live-only difference is entrance
 * motion and the open-group treatment (`turnActive`).
 */
export function ChatRenderer({
  rows,
  links = [],
  viewedSessionId,
  linkName,
  attachmentUrl,
  turnActive = false,
  foldNewCode = false,
  toolStatuses,
  background,
  tasks,
  selectedToolId,
  onOpenTool,
  onOpenTask,
  worktreeCards = [],
  worktreeActions,
  permissionRequests = [],
  onResolvePermission = noResolve,
  permissionReveal,
  onRetry,
  sendFailure,
  find,
  onFindStatus,
}: ChatRendererProps) {
  const linksById = new Map(links.map((l) => [l.id, l]));
  // Adjacent text joins into one bubble (§6), then failed sends fold.
  const { rows: shown, delivery } = collapseFailedSends(joinRows(rows), sendFailure);
  const latestSend = [...shown].reverse().find((m) => m.role === "user" && !m.attribution);

  const cardsById = new Map(worktreeCards.map((c) => [c.id, c]));
  const items = buildItems(interleaveCards(rowsToEntries(shown), worktreeCards));
  const permissions = anchorPermissions(items, permissionRequests);
  // A fallback (unanchored) approval that lands after mount folds in and rings once.
  const arrivedPermission = useArrivals(permissions.unanchored.map((r) => r.id));

  // Collapse digest→NOOP pairs into quiet-check chips before rendering. A
  // completed (non-live) agent reply of exactly NOOP after a digest card —
  // directly, or past thinking-only activity — is the monitor saying
  // "nothing to do"; showing both in full would pay the transcript cost the
  // noop protocol exists to avoid.
  //
  // A notice is not a quiet check (it isn't a digest, and folding it in hid
  // the grant notice): a NOOP answering one is folded into the notice row
  // itself as "acknowledged" instead.
  const rendered: (ReturnType<typeof buildItems>[number] | QuietItem)[] = [];
  const acknowledged = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    const j = item.type === "message" ? noopReplyAt(items, i + 1) : -1;
    if (item.type === "message" && j >= 0 && isNotice(item.message)) {
      acknowledged.add(item.key);
      rendered.push(item);
      i = j;
      continue;
    }
    // A coalesced run (several digests delivered as one turn) is answered
    // by one NOOP: the whole run folds into the chip.
    let end = i + 1;
    while (end < items.length) {
      const next = items[end];
      if (next.type !== "message" || next.message.role !== "user" || !isDigestCard(next.message)) break;
      end++;
    }
    const runReply = item.type === "message" && isDigestCard(item.message) ? noopReplyAt(items, end) : -1;
    if (runReply >= 0) {
      for (let k = i; k < end; k++) {
        const m = (items[k] as Extract<(typeof items)[number], { type: "message" }>).message;
        const digest: QuietDigest = {
          key: items[k].key,
          source: m.attribution!.source_name,
          kind: kindOf(m.attribution!) === "signal" ? "signal" : "digest",
          time: parseMessageTime(m.timestamp),
          text: m.text ?? "",
          cadence: digestLink(m.attribution!, links, viewedSessionId, linkName)?.cadence,
        };
        const last = rendered[rendered.length - 1];
        if (last?.type === "quiet") {
          last.digests.push(digest);
        } else {
          rendered.push({ key: items[k].key, type: "quiet", digests: [digest] });
        }
      }
      i = runReply; // the thinking rows and the NOOP reply are consumed by the chip
      continue;
    }
    rendered.push(item);
  }

  // Folding a digest and its NOOP into a pill drops the stream's height in
  // one frame. Measure the drop and hand it to the pill as a margin that
  // shrinks away (QuietChecks), so what's below glides up; the scroller's
  // stick-to-bottom follows the shrinking height and the browser's scroll
  // anchoring keeps a reader scrolled up in place.
  const rootRef = useRef<HTMLDivElement>(null);
  const quietTotal = rendered.reduce((n, r) => n + (r.type === "quiet" ? r.digests.length : 0), 0);
  const lastQuiet = [...rendered].reverse().find((r) => r.type === "quiet");
  const measured = useRef<{ height: number; quiet: number } | null>(null);
  const [easing, setEasing] = useState<{ key: string; px: number } | null>(null);
  // Every commit measures against the last one; it sets state only when a
  // pill grew and the stream shrank, so it settles.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    const height = rootRef.current?.offsetHeight ?? 0;
    const prev = measured.current;
    measured.current = { height, quiet: quietTotal };
    if (prev && lastQuiet && quietTotal > prev.quiet && height < prev.height) {
      setEasing({ key: lastQuiet.key, px: prev.height - height });
    }
  });
  useEffect(() => {
    if (!easing) return;
    const t = window.setTimeout(() => setEasing(null), FOLD_MS);
    return () => window.clearTimeout(t);
  }, [easing]);

  // Find: activity rows and compaction summaries don't render their text, so
  // their matches come from the transcript; everything else is searched as
  // rendered (useFind).
  const query = find?.query ?? "";
  const matchCase = Boolean(find?.matchCase);
  const skipTools = Boolean(find?.skipTools);
  // The cache is per pattern: matches found for one query are no good for the next.
  const { pattern, cache } = useMemo(
    () => ({ pattern: findPattern({ query, matchCase }), cache: new Map() as SearchCache }),
    [query, matchCase],
  );
  const groupMatches = new Map<string, EntryMatch[]>();
  const summaryMatches = new Map<string, number>();
  if (pattern) {
    for (const item of rendered) {
      if (item.type === "activity") {
        const found = searchEntries(item.entries, pattern, skipTools, cache);
        if (found.length > 0) groupMatches.set(item.key, found);
      } else if (item.type === "message" && item.message.role === "compact" && item.message.text) {
        const n = findAll(item.message.text, pattern).length;
        if (n > 0) summaryMatches.set(item.key, n);
      }
    }
  }
  const findTarget = useFind(rootRef, find, groupMatches, summaryMatches, onFindStatus);

  return (
    <div className={styles.stream} ref={rootRef} data-chat-rows>
      {rendered.map((item) => {
        if (item.type === "quiet") {
          return <QuietChecks key={item.key} item={item} ease={easing?.key === item.key ? easing.px : undefined} />;
        }
        if (item.type === "activity") {
          return (
            <ActivityGroup
              key={item.key}
              entries={item.entries}
              findKey={item.key}
              findMatches={groupMatches.get(item.key)}
              findCurrent={findTarget?.kind === "entry" && findTarget.itemKey === item.key ? findTarget : undefined}
              live={Boolean(item.live && turnActive)}
              statuses={toolStatuses}
              background={background}
              selectedToolId={selectedToolId}
              onOpenTool={onOpenTool}
              permissions={permissions.anchored}
              onResolvePermission={onResolvePermission}
              reveal={permissionReveal}
            />
          );
        }
        if (item.type === "plan") {
          return <PlanWidget key={item.key} todos={item.todos} findKey={item.key} />;
        }
        if (item.type === "card") {
          const card = cardsById.get(item.id);
          return card ? (
            <div key={item.key} className={styles.findable} data-find={item.key}>
              <WorktreeCard card={card} actions={worktreeActions} />
            </div>
          ) : null;
        }
        if (item.type === "task") {
          const model = item.tool.id ? tasks?.get(item.tool.id) : undefined;
          return (
            <div key={item.key} className={styles.findable} data-find={item.key} data-find-tool>
              <TaskCard
                tool={item.tool}
                model={model}
                status={item.tool.id ? toolStatuses?.get(item.tool.id) : undefined}
                permissions={item.tool.id ? permissions.anchored.get(item.tool.id) : undefined}
                onResolvePermission={onResolvePermission}
                onOpen={
                  onOpenTask && item.tool.id ? () => onOpenTask(item.tool.id!, item.tool) : undefined
                }
              />
            </div>
          );
        }
        const message = item.message;
        // Injected messages carry role "user" at the transport level, but
        // must never render as the subject user's own turn (decision 2,
        // docs/LINKED-SESSIONS-DESIGN.md) — attribution wins over role.
        // What the message is comes from its wrapper's kind: a notice from
        // mads itself (e.g. a pipeline telling the agent which files a
        // merge changed under it — never the user's voice, and not gated
        // on linking), a digest or signal (a card: only the watching side
        // ever receives one), or a linked session's message (a chip naming
        // the sender by relation).
        if (message.attribution && isNotice(message)) {
          return (
            <NoticeRow
              key={item.key}
              findKey={item.key}
              attribution={message.attribution}
              text={message.text ?? ""}
              acknowledged={acknowledged.has(item.key)}
            />
          );
        }
        if (message.attribution && FLAGS.linkedSessions) {
          if (isDigestCard(message)) {
            return (
              <DigestCard
                key={item.key}
                findKey={item.key}
                source={message.attribution.source_name}
                kind={kindOf(message.attribution) === "signal" ? "signal" : "digest"}
                time={parseMessageTime(message.timestamp)}
                text={message.text ?? ""}
                cadence={digestLink(message.attribution, links, viewedSessionId, linkName)?.cadence}
              />
            );
          }
          return (
            <div key={item.key} className={styles.injected} data-find={item.key}>
              <Chip tone="info">
                ⇄ from {message.attribution.relation ?? "a linked session"} · {message.attribution.source_name}
              </Chip>
              <Markdown className={styles.prose} text={message.text ?? ""} />
            </div>
          );
        }
        if (message.role === "compact") {
          return (
            <CompactionMarker
              key={item.key}
              findKey={item.key}
              findMatches={summaryMatches.get(item.key)}
              findOpen={findTarget?.kind === "summary" && findTarget.itemKey === item.key}
              marker={message.compact ?? {}}
              summary={message.text}
              timestamp={message.timestamp}
            />
          );
        }
        if (message.role === "inbound" && message.inbound) {
          // From outside MADS, not through a link: the link (when one
          // resolves) only names the relationship, never the delivery.
          const link = message.inbound.link_id ? linksById.get(message.inbound.link_id) : undefined;
          const relation = link
            ? link.subject_session_id === viewedSessionId
              ? "your leader"
              : "linked to this session"
            : undefined;
          return (
            <InboundMessage
              key={item.key}
              text={message.text}
              inbound={message.inbound}
              timestamp={message.timestamp}
              relation={relation}
              findKey={item.key}
            />
          );
        }
        if (message.notice) {
          // A row the agent wrote itself rather than said (an interrupt
          // marker, a failed turn): a quiet system line, not its voice.
          return (
            <div key={item.key} className={styles.notification} role="note" data-find={item.key}>
              <span aria-hidden="true">⏺</span>
              <span className={styles.notificationText} title={message.text}>
                {message.text}
              </span>
            </div>
          );
        }
        if (message.role === "notification") {
          // The harness telling the agent a background task finished, which
          // can start a turn nobody typed. Drawn as a quiet system line, not
          // in the human's voice: as a user bubble it was a wall of raw
          // <task-notification> XML the human never wrote.
          const at = parseMessageTime(message.timestamp);
          return (
            <div key={item.key} className={styles.notification} role="note" data-find={item.key}>
              <span aria-hidden="true">⏺</span>
              <span className={styles.notificationText} title={message.text}>
                {message.text || "Background task finished"}
              </span>
              {at && (
                <time className={styles.notificationTime} dateTime={at.toISOString()} title={at.toLocaleString()}>
                  {formatMessageTime(at)}
                </time>
              )}
            </div>
          );
        }
        const at = parseMessageTime(message.timestamp);
        const stamp = at && (
          <time className={styles.stamp} dateTime={at.toISOString()} title={at.toLocaleString()}>
            {formatMessageTime(at)}
          </time>
        );
        const isUser = message.role === "user";
        const { body, attachments } = isUser
          ? splitAttachments(message.text ?? "")
          : { body: message.text ?? "", attachments: [] };
        const content =
          attachments.length > 0 ? (
            <div className={styles.withAttachments}>
              <MessageAttachments attachments={attachments} attachmentUrl={attachmentUrl} />
              {body && <Markdown className={styles.prose} text={body} breaks />}
            </div>
          ) : (
            <Markdown
              className={styles.prose}
              text={body}
              breaks={isUser}
              foldNewCode={foldNewCode && Boolean(item.live)}
            />
          );
        const sent = isUser ? delivery.get(item.key) : undefined;
        const retry =
          sent?.error && onRetry && !turnActive && item.key === latestSend?.key
            ? () => onRetry(message.text ?? "")
            : undefined;
        return (
          <div
            key={item.key}
            className={styles[message.role] ?? styles.agent}
            data-find={item.key}
            // Anchor for the Ctrl+↑/↓ "hop between my messages" shortcut.
            data-user-msg={isUser ? "" : undefined}
            data-undelivered={sent?.error ? "" : undefined}
          >
            {/* Revealed on hover (styles.stamp), on the bubble's open side. */}
            {isUser && stamp}
            {sent ? (
              <div className={styles.sent}>
                {content}
                <SendDelivery delivery={sent} onRetry={retry} />
              </div>
            ) : (
              content
            )}
            {!isUser && stamp}
          </div>
        );
      })}
      {permissions.unanchored.map((req) => {
        const fresh = arrivedPermission(req.id);
        return (
          <Fold key={req.id} appear={fresh} fade>
            <PermissionCard request={req} onResolve={onResolvePermission} ring={fresh} />
          </Fold>
        );
      })}
    </div>
  );
}
