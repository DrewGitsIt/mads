import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { RouterClient } from "../../router/client";
import type { QueuedMessage, Session, Usage } from "../../router/types";
import { useMentionResolver, type MentionCandidate } from "./useMentionResolver";
import { MentionMenu, mentionOptionId } from "./MentionMenu";
import { InjectedContentPreview } from "./InjectedContentPreview";
import { ExpandedComposer } from "./ExpandedComposer";
import { ComposerPlusMenu, type ComposerMode } from "./ComposerPlusMenu";
import { getDraft, setDraft } from "./composerDrafts";
import { AttachmentChips } from "../../components/Attachments/AttachmentChips";
import { stagedForSession } from "../../components/Attachments/stagedAttachments";
import { useStagedAttachments } from "../../components/Attachments/useStagedAttachments";
import { useFileDrop } from "../../components/Attachments/useFileDrop";
import { appendAttachments, splitAttachments } from "../attachmentRefs";
import { matches } from "../../platform/hotkeys";
import styles from "./Composer.module.css";

export type { ComposerMode } from "./ComposerPlusMenu";

const EMPTY_QUEUE: QueuedMessage[] = [];

export interface ComposerProps {
  routerClient: RouterClient;
  sessionId: string;
  agentId?: string;
  /** The session's current provider — resolves the "/model" command's candidate list. */
  provider?: string;
  /**
   * The last turn's usage and the model's fallback window, for the + menu's
   * context row (the dock's rim is the ambient meter). Absent → no row.
   */
  usage?: Usage;
  contextWindow?: number;
  /** The conversation's current view. Default "chat". */
  mode?: ComposerMode;
  /**
   * Switches Chat ⇄ Terminal: the + menu's View section and the toggleView
   * hotkey. Absent → no View section and the hotkey is left alone.
   */
  onModeChange?: (mode: ComposerMode) => void;
  /** Compacts the conversation now — the + menu's "Compact now". Absent → hidden. */
  onCompact?: () => void;
  onSent?: (text: string, mentions: string[]) => void;
  /** Disables the textarea and Send button — set during a transport switch. */
  disabled?: boolean;
  /**
   * A turn is in flight. The composer stays usable: a send mid-turn is queued
   * by the session process (see handleSend) rather than interleaving with the
   * agent's response. Here it decides whether Stop shows and whether the
   * send button reads "Queue" instead of "Send".
   */
  busy?: boolean;
  /** The session's queued messages, owned by the session process and mirrored by the caller. */
  queued?: QueuedMessage[];
  /** A send was queued behind the in-flight turn instead of run (no user bubble yet). */
  onQueued?: (queued: QueuedMessage) => void;
  /** The user removed a queued message before it went out. */
  onCancelQueued?: (queuedId: string) => void;
  /**
   * Interrupts the in-flight turn (chat mode's ⌘/Ctrl-C). When set and `busy`,
   * a Stop button appears alongside Queue.
   */
  onStop?: () => void;
  /** Seeds the textarea on first mount only — later changes to this prop are ignored. */
  initialValue?: string;
  /**
   * Replaces the textarea's text and focuses it, never sends — each new
   * `nonce` applies once ("Ask the agent to resolve" drafts a message the
   * user can read and edit before it goes).
   */
  prefill?: { text: string; nonce: number };
  /** Fires handleSend once on mount when `initialValue` is set — used to auto-send a session's opening goal. */
  autoSendInitial?: boolean;
  onSendError?: (message: string) => void;
  /**
   * Fires synchronously when the auto-send effect runs, before the send
   * resolves — lets the caller stop treating `initialValue` as pending now
   * that this Composer instance owns it (in its textarea either way,
   * success or failure). Must not be tied to the *caller's* mount, since a
   * gate like ConversationTab's `if (!session) return …` means Composer
   * mounts asynchronously, well after its parent.
   */
  onAutoSendStart?: () => void;
  /** Fires synchronously at the start of every send (manual or auto) — lets the caller show an in-flight indicator before the request resolves. */
  onSendStart?: () => void;
  /**
   * Applies a "/agent" or "/model" command (the same commit path the dock's
   * persona/model pickers use). Resolves true when the change was accepted
   * but queued behind an in-flight turn — a model switch never interrupts a
   * running agent, so the confirmation has to say "applies when the current
   * turn finishes" rather than claiming it already took effect. Rejects on
   * failure, surfaced through onCommandError.
   */
  onUpdateSession?: (patch: Partial<Pick<Session, "agent" | "model">>) => void | Promise<boolean | void>;
  /** Fires once a "/agent"/"/model" command has been applied — surface as a toast, not a transcript message. */
  onCommandExecuted?: (message: string) => void;
  /** Fires when onUpdateSession throws for a "/agent"/"/model" command. */
  onCommandError?: (message: string) => void;
}

/**
 * The session dock's chat input (mockup E): attachment and mention chips,
 * then `[+]` · borderless auto-growing textarea · `[■]` (busy) · Send / Queue.
 * Renders content only — the dock around it supplies the surface, the rim
 * (context meter) and the status line. The root carries `data-composer` as a
 * `:focus-within` hook for the dock's focus treatment.
 *
 * The expandEditor hotkey (⌘E on mac, Ctrl+Shift+E elsewhere; see
 * platform/hotkeys.ts) expands in place into a line-numbered Monaco editor
 * (no modal, MADS-UX §3.3). @/ trigger resolution is delegated to
 * useMentionResolver and only ever rendered against the collapsed textarea —
 * the expanded editor is plain-text markdown compose only. `@` mentions
 * splice a plain-text reference token into the value and stage it in
 * InjectedContentPreview — nothing is fetched/expanded until send (§2.4).
 * `/` commands are a hierarchical palette: a plain leaf (skill/task) inserts
 * text the same way but never stages a chip; an argument-taking command
 * ("agent"/"model") descends to a second level and its leaves dispatch
 * directly (executeCommand) instead of touching the message at all.
 */
export function Composer({
  routerClient,
  sessionId,
  agentId,
  provider,
  usage,
  contextWindow,
  mode = "chat",
  onModeChange,
  onCompact,
  onSent,
  disabled,
  busy,
  onStop,
  initialValue,
  prefill,
  autoSendInitial,
  onSendError,
  onAutoSendStart,
  onSendStart,
  onUpdateSession,
  onCommandExecuted,
  onCommandError,
  queued = EMPTY_QUEUE,
  onQueued,
  onCancelQueued,
}: ComposerProps) {
  // Seed from a saved draft (survives navigating away and back) unless the
  // caller handed us an initialValue to auto-send.
  const [value, setValue] = useState(initialValue ?? getDraft(sessionId));
  const [cursor, setCursor] = useState(0);

  // Mirror the current text into the per-session draft store on every change,
  // so it's there when this Composer remounts for the same session. Clears
  // itself when the value goes empty (e.g. after a send).
  useEffect(() => {
    setDraft(sessionId, value);
  }, [sessionId, value]);
  const [expanded, setExpanded] = useState(false);
  const [injected, setInjected] = useState<MentionCandidate[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  // Set by Escape at the command menu's root level — cleared the moment the
  // user edits the text again, so dismissing never "sticks" past a keystroke.
  const [menuDismissed, setMenuDismissed] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  // Pasted, dropped and picked files upload the moment they're staged and
  // outlive this mount (stagedAttachments.ts), like the text draft above.
  const staged = stagedForSession(sessionId, routerClient);
  const attachments = useStagedAttachments(staged);
  const uploading = attachments.some((a) => a.status === "uploading");
  const { dragging, dropHandlers, onPaste } = useFileDrop((files) => staged.add(files));

  const { trigger, candidates } = useMentionResolver(
    routerClient,
    agentId,
    provider,
    value,
    cursor,
  );
  const menuOpen = !expanded && Boolean(trigger) && !menuDismissed && candidates.length > 0;
  // A narrowing filter can shrink candidates out from under a higher
  // activeIndex (e.g. arrowed to #4, then typed a letter that leaves 2
  // matches) — reset on every list change so selection/aria-activedescendant
  // never index past the end.
  useEffect(() => {
    setActiveIndex(0);
  }, [candidates]);

  function focusTextareaAt(offset: number) {
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(offset, offset);
    });
  }

  function selectCandidate(candidate: MentionCandidate) {
    if (!trigger) return;
    if (candidate.kind === "command-action") {
      void executeCommand(candidate, trigger.start);
      return;
    }
    const before = value.slice(0, trigger.start);
    const after = value.slice(cursor);
    const insertion = `${candidate.insertText} `;
    const nextValue = `${before}${insertion}${after}`;
    const nextCursor = before.length + insertion.length;
    setValue(nextValue);
    setCursor(nextCursor);
    // Only @-mentions/attachments stage into the chip strip — a command
    // (skill or otherwise) is either plain text for the agent or, for
    // command-action leaves above, dispatched directly with no chip at all.
    if (candidate.kind === "mentionable") {
      setInjected((prev) =>
        prev.some((p) => p.id === candidate.id) ? prev : [...prev, candidate],
      );
    }
    setActiveIndex(0);
    focusTextareaAt(nextCursor);
  }

  // Executes a level-2 leaf (rebind agent / switch model) against the
  // session, wiping the whole "/agent foo" (or "/model foo") chunk out of
  // the composer — never leaves residual command text or a chip behind.
  async function executeCommand(candidate: MentionCandidate, triggerStart: number) {
    const before = value.slice(0, triggerStart);
    const after = value.slice(cursor);
    const nextValue = `${before}${after}`;
    const nextCursor = before.length;
    setValue(nextValue);
    setCursor(nextCursor);
    setActiveIndex(0);
    focusTextareaAt(nextCursor);
    try {
      const queued = await onUpdateSession?.(
        candidate.group === "agent" ? { agent: candidate.id } : { model: candidate.id },
      );
      onCommandExecuted?.(
        `${candidate.group === "agent" ? "Agent" : "Model"} set to ${candidate.label}` +
          (queued ? " — applies when the current turn finishes." : ""),
      );
    } catch (err) {
      onCommandError?.(err instanceof Error ? err.message : String(err));
    }
  }

  // The + menu's Mention / Commands: types the trigger character at the
  // caret (collapsing the editor first — triggers resolve only against the
  // textarea), which opens the existing mention or command menu.
  function insertTrigger(ch: "@" | "/") {
    const at = Math.min(cursor, value.length);
    const before = value.slice(0, at);
    const pad = before && !/\s$/.test(before) ? " " : "";
    const nextValue = `${before}${pad}${ch}${value.slice(at)}`;
    const nextCursor = at + pad.length + 1;
    setExpanded(false);
    setValue(nextValue);
    setCursor(nextCursor);
    setMenuDismissed(false);
    focusTextareaAt(nextCursor);
  }

  function openFilePicker() {
    if (disabled) return;
    fileInputRef.current?.click();
  }

  // The composer's hotkeys (expandEditor, attachFiles, toggleView) are
  // handled by the wrapping div's onKeyDownCapture below: it must fire before
  // Monaco's own keybinding service sees the event while expanded — Monaco
  // has no onKeyDown prop, only addCommand from inside onMount — so a single
  // outer capture is what lets one binding work from both collapsed and
  // expanded focus.
  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (trigger && !menuDismissed && e.key === "Escape") {
      e.preventDefault();
      if (trigger.type === "/") {
        const spaceIdx = trigger.query.indexOf(" ");
        if (spaceIdx !== -1) {
          // Step back from level 2 to level 1 — drop the argument, keep the command.
          const head = trigger.query.slice(0, spaceIdx);
          const nextValue = `${value.slice(0, trigger.start)}/${head}${value.slice(cursor)}`;
          const nextCursor = trigger.start + 1 + head.length;
          setValue(nextValue);
          setCursor(nextCursor);
          focusTextareaAt(nextCursor);
          return;
        }
      }
      setMenuDismissed(true);
      return;
    }

    if (menuOpen) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIndex((i) => (i + 1) % candidates.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIndex((i) => (i - 1 + candidates.length) % candidates.length);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        const candidate = candidates[activeIndex];
        if (candidate) selectCandidate(candidate);
        return;
      }
    }

    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
    }
  }

  // Every send asks to queue: mid-turn, the session process holds the
  // message and delivers it when the turn ends — so it still goes out if
  // this pane unmounts first — and an idle session just runs it. Asking
  // whatever `busy` says here also covers a turn this pane hasn't seen yet.
  //
  // Staged attachments go by id; the Router appends their paths to the text
  // the agent gets (services/router/internal/attachments). A send waits for
  // every upload to land — Send is disabled meanwhile — so none is dropped.
  async function handleSend() {
    const text = value.trim();
    const ready = attachments.flatMap((a) =>
      a.status === "ready" && a.attachment ? [a.attachment] : [],
    );
    if ((!text && ready.length === 0) || staged.uploading()) return;
    const mentions = injected.map((i) => i.id);
    onSendStart?.();
    try {
      const result = await routerClient.sendMessage(sessionId, {
        text,
        mentions,
        queue: true,
        ...(ready.length > 0 ? { attachments: ready.map((a) => a.id) } : {}),
      });
      if (result.status === "queued") onQueued?.(result.queued);
      else onSent?.(appendAttachments(text, ready), mentions);
    } catch (err) {
      onSendError?.(err instanceof Error ? err.message : String(err));
      return;
    }
    setValue("");
    setCursor(0);
    setInjected([]);
    staged.clear();
  }

  useEffect(() => {
    if (!prefill) return;
    setValue(prefill.text);
    setCursor(prefill.text.length);
    setExpanded(false);
    focusTextareaAt(prefill.text.length);
    // Keyed on the nonce alone: the same text asked for twice applies twice.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prefill?.nonce]);

  const autoSentRef = useRef(false);
  useEffect(() => {
    if (autoSendInitial && initialValue && !autoSentRef.current) {
      autoSentRef.current = true;
      onAutoSendStart?.();
      void handleSend();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- auto-send is a one-time mount effect, guarded by autoSentRef against StrictMode's double-invoke
  }, []);

  // Returns focus to the textarea, at the same offset Monaco's caret was at,
  // the moment expanded flips back to false — never fires on initial mount
  // (prevExpandedRef starts false) or while expanding.
  const prevExpandedRef = useRef(false);
  useEffect(() => {
    if (prevExpandedRef.current && !expanded) {
      requestAnimationFrame(() => {
        textareaRef.current?.focus();
        textareaRef.current?.setSelectionRange(cursor, cursor);
      });
    }
    prevExpandedRef.current = expanded;
  }, [expanded, cursor]);

  function handleComposerKeyDownCapture(e: KeyboardEvent<HTMLDivElement>) {
    if (matches("expandEditor", e)) {
      e.preventDefault();
      e.stopPropagation();
      setExpanded((x) => !x);
    } else if (matches("attachFiles", e)) {
      e.preventDefault();
      e.stopPropagation();
      openFilePicker();
    } else if (onModeChange && matches("toggleView", e)) {
      e.preventDefault();
      e.stopPropagation();
      onModeChange(mode === "terminal" ? "chat" : "terminal");
    }
  }

  // Both layouts lead with the same + menu, so attach, mention, commands
  // and the view switch are one click away whether or not the editor is open.
  const plusMenu = (
    <ComposerPlusMenu
      expanded={expanded}
      disabled={disabled}
      onAttach={openFilePicker}
      onMention={() => insertTrigger("@")}
      onCommands={() => insertTrigger("/")}
      onToggleExpanded={() => setExpanded((x) => !x)}
      mode={mode}
      onModeChange={onModeChange}
      usage={usage}
      contextWindow={contextWindow}
      onCompact={onCompact}
    />
  );
  const sendBlockedReason = uploading ? "Waiting for attachments to finish uploading" : undefined;

  return (
    <div
      className={[styles.composer, dragging && styles.dropTarget].filter(Boolean).join(" ")}
      data-composer=""
      onKeyDownCapture={handleComposerKeyDownCapture}
      onPaste={onPaste}
      {...dropHandlers}
    >
      {queued.length > 0 && (
        <div className={styles.queued}>
          {queued.map((q) => (
            <div key={q.id} className={styles.queuedItem}>
              <span className={styles.queuedBadge}>Queued</span>
              <span className={styles.queuedText}>{queuedLabel(q.text)}</span>
              <button
                type="button"
                className={styles.queuedRemove}
                aria-label="Remove queued message"
                onClick={() => onCancelQueued?.(q.id)}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      <div className={styles.chips}>
        <InjectedContentPreview
          items={injected}
          onRemove={(id) => setInjected((prev) => prev.filter((p) => p.id !== id))}
        />
        <AttachmentChips
          items={attachments}
          thumbnailUrl={(a) =>
            a.attachment?.thumbnail
              ? routerClient.attachmentUrl(staged.owner, a.attachment.id, "thumbnail")
              : undefined
          }
          onRemove={(key) => staged.remove(key)}
        />
      </div>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        aria-label="Attach files"
        onChange={(e) => {
          staged.add(Array.from(e.target.files ?? []));
          e.target.value = "";
        }}
      />
      {expanded ? (
        <ExpandedComposer
          value={value}
          onChange={setValue}
          onSend={() => void handleSend()}
          onCollapse={() => setExpanded(false)}
          initialCursor={cursor}
          onCursorChange={setCursor}
          disabled={disabled}
          busy={busy}
          onStop={onStop}
          sendBlockedReason={sendBlockedReason}
          leading={plusMenu}
        />
      ) : (
        <div className={styles.inputRow}>
          {menuOpen && (
            <MentionMenu
              candidates={candidates}
              activeIndex={activeIndex}
              onSelect={selectCandidate}
            />
          )}
          {plusMenu}
          <textarea
            ref={textareaRef}
            className={styles.textarea}
            rows={1}
            placeholder="Message the agent — @ to mention, / for commands"
            value={value}
            disabled={disabled}
            aria-activedescendant={
              // A resolved candidate list can commit before the activeIndex-reset
              // effect below runs, momentarily leaving activeIndex past the end
              // of a just-narrowed array — guard the read rather than crash mid-render.
              menuOpen && candidates[activeIndex]
                ? mentionOptionId(candidates[activeIndex].id)
                : undefined
            }
            onChange={(e) => {
              setValue(e.target.value);
              setCursor(e.target.selectionStart ?? e.target.value.length);
              setMenuDismissed(false);
            }}
            onKeyUp={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
            onClick={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
            onKeyDown={handleKeyDown}
          />
          {busy && onStop && (
            <button
              type="button"
              className={styles.stop}
              title="Stop generating"
              aria-label="Stop generating"
              onClick={onStop}
            >
              ■
            </button>
          )}
          <button
            type="button"
            className={[styles.send, busy && styles.queue].filter(Boolean).join(" ")}
            onClick={() => void handleSend()}
            disabled={disabled || uploading}
            title={
              sendBlockedReason ??
              (busy ? "Queue — sends when the current turn finishes" : undefined)
            }
          >
            {busy ? "Queue" : "Send"}
          </button>
        </div>
      )}
    </div>
  );
}

/** A queued message's text without the attachment paths the Router appended. */
function queuedLabel(text: string): string {
  const { body, attachments } = splitAttachments(text);
  if (attachments.length === 0) return text;
  const files = `📎 ${attachments.length} file${attachments.length === 1 ? "" : "s"}`;
  return body ? `${body} · ${files}` : files;
}
