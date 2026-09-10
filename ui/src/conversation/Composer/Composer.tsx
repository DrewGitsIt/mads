import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Button } from "../../components/Button/Button";
import type { RouterClient } from "../../router/client";
import type { Session } from "../../router/types";
import { useMentionResolver, type MentionCandidate } from "./useMentionResolver";
import { MentionMenu, mentionOptionId } from "./MentionMenu";
import { InjectedContentPreview } from "./InjectedContentPreview";
import { ExpandedComposer } from "./ExpandedComposer";
import { getDraft, setDraft } from "./composerDrafts";
import styles from "./Composer.module.css";

export interface ComposerProps {
  routerClient: RouterClient;
  sessionId: string;
  agentId?: string;
  /** The session's current provider — resolves the "/model" command's candidate list. */
  provider?: string;
  onSent?: (text: string, mentions: string[]) => void;
  /** Disables the textarea and Send button — set during a transport switch. */
  disabled?: boolean;
  /**
   * A turn is in flight. The composer stays usable, but a send is held in a
   * local queue and flushed automatically once `busy` clears (the turn ends),
   * rather than interleaving with the agent's response.
   */
  busy?: boolean;
  /**
   * Interrupts the in-flight turn (chat mode's ⌘/Ctrl-C). When set and `busy`,
   * a Stop button appears alongside Send; Send keeps queueing as before.
   */
  onStop?: () => void;
  /** Seeds the textarea on first mount only — later changes to this prop are ignored. */
  initialValue?: string;
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
  /** Same commit path ConversationHeader's agent/model pills use — a "/agent"/"/model" command dispatches through here rather than sending a chat message. */
  /**
   * Applies a "/agent" or "/model" command. Resolves true when the change was
   * accepted but queued behind an in-flight turn — a model switch never
   * interrupts a running agent, so the confirmation has to say "applies when
   * the current turn finishes" rather than claiming it already took effect.
   * Rejects on failure, surfaced through onCommandError.
   */
  onUpdateSession?: (patch: Partial<Pick<Session, "agent" | "model">>) => void | Promise<boolean | void>;
  /** Fires once a "/agent"/"/model" command has been applied — surface as a toast, not a transcript message. */
  onCommandExecuted?: (message: string) => void;
  /** Fires when onUpdateSession throws for a "/agent"/"/model" command. */
  onCommandError?: (message: string) => void;
}

/**
 * Plain textarea, ⌘E expands in place into a Monaco editor (no modal,
 * MADS-UX §3.3). @/ trigger resolution is delegated to useMentionResolver
 * and only ever rendered against the collapsed textarea — the expanded
 * Monaco editor is plain-text markdown compose only (HANDOFF.md "Edge Cases
 * & TODOs for Future"). `@` mentions splice a plain-text reference token
 * into the value and stage it in InjectedContentPreview — nothing is
 * fetched/expanded until send (§2.4). `/` commands are a hierarchical
 * palette: a plain leaf (skill/task) inserts text the same way but never
 * stages a chip; an argument-taking command ("agent"/"model") descends to a
 * second level and its leaves dispatch directly (executeCommand) instead of
 * touching the message at all.
 */
export function Composer({
  routerClient,
  sessionId,
  agentId,
  provider,
  onSent,
  disabled,
  busy,
  onStop,
  initialValue,
  autoSendInitial,
  onSendError,
  onAutoSendStart,
  onSendStart,
  onUpdateSession,
  onCommandExecuted,
  onCommandError,
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
    requestAnimationFrame(() => {
      textareaRef.current?.setSelectionRange(nextCursor, nextCursor);
      textareaRef.current?.focus();
    });
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
    requestAnimationFrame(() => {
      textareaRef.current?.setSelectionRange(nextCursor, nextCursor);
      textareaRef.current?.focus();
    });
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

  // ⌘E/Ctrl+E is handled by the wrapping div's onKeyDownCapture below (it
  // must fire before Monaco's own keybinding service sees the event while
  // expanded — Monaco has no onKeyDown prop, only addCommand from inside
  // onMount, so a single outer capture is what lets one toggle work from
  // both collapsed and expanded focus).
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
          requestAnimationFrame(() => {
            textareaRef.current?.setSelectionRange(nextCursor, nextCursor);
            textareaRef.current?.focus();
          });
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

  // Messages composed while a turn is in flight — flushed in order once `busy`
  // clears (see the effect below), so a send never interleaves with the
  // agent's response.
  const [queued, setQueued] = useState<{ text: string; mentions: string[] }[]>([]);
  const flushingRef = useRef(false);

  async function sendNow(text: string, mentions: string[]): Promise<boolean> {
    onSendStart?.();
    try {
      await routerClient.sendMessage(sessionId, { text, mentions });
      onSent?.(text, mentions);
      return true;
    } catch (err) {
      onSendError?.(err instanceof Error ? err.message : String(err));
      return false;
    }
  }

  async function handleSend() {
    const text = value.trim();
    if (!text) return;
    const mentions = injected.map((i) => i.id);
    if (busy) {
      // Queue behind the in-flight turn; the input clears now so it reads as
      // "handed off", and the flush effect sends it when the turn ends.
      setQueued((q) => [...q, { text, mentions }]);
      setValue("");
      setCursor(0);
      setInjected([]);
      return;
    }
    if (await sendNow(text, mentions)) {
      setValue("");
      setCursor(0);
      setInjected([]);
    }
  }

  // Flush one queued message whenever the turn is idle. Sending it starts a new
  // turn (busy → true again), so items drain one per turn, in order.
  useEffect(() => {
    if (busy || disabled || queued.length === 0 || flushingRef.current) return;
    flushingRef.current = true;
    const [head, ...rest] = queued;
    setQueued(rest);
    void sendNow(head.text, head.mentions).finally(() => {
      flushingRef.current = false;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- sendNow is stable enough for this drain; deps intentionally limited to the drain triggers
  }, [busy, disabled, queued]);

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
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "e") {
      e.preventDefault();
      e.stopPropagation();
      setExpanded((x) => !x);
    }
  }

  const expandTitle = expanded ? "Collapse editor (⌘E)" : "Expand editor (⌘E)";

  return (
    <div className={styles.composer} onKeyDownCapture={handleComposerKeyDownCapture}>
      <InjectedContentPreview
        items={injected}
        onRemove={(id) => setInjected((prev) => prev.filter((p) => p.id !== id))}
      />
      {queued.length > 0 && (
        <div className={styles.queued}>
          {queued.map((q, i) => (
            <div key={i} className={styles.queuedItem}>
              <span className={styles.queuedBadge}>Queued</span>
              <span className={styles.queuedText}>{q.text}</span>
              <button
                type="button"
                className={styles.queuedRemove}
                aria-label="Remove queued message"
                onClick={() => setQueued((cur) => cur.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      {expanded ? (
        <div className={styles.expandedRow}>
          <ExpandedComposer
            value={value}
            onChange={setValue}
            onSend={() => void handleSend()}
            onCollapse={() => setExpanded(false)}
            initialCursor={cursor}
            onCursorChange={setCursor}
            disabled={disabled}
          />
          <div className={styles.actions}>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              title={expandTitle}
              aria-label="Collapse editor"
              aria-keyshortcuts="Meta+e"
              onClick={() => setExpanded(false)}
              disabled={disabled}
            >
              ⤡
            </Button>
            {busy && onStop && (
              <Button
                type="button"
                variant="secondary"
                className={styles.stop}
                title="Stop generating"
                aria-label="Stop generating"
                onClick={onStop}
              >
                ■ Stop
              </Button>
            )}
            <Button variant="primary" onClick={() => void handleSend()} disabled={disabled}>
              Send
            </Button>
          </div>
        </div>
      ) : (
        <div className={styles.inputRow}>
          {menuOpen && (
            <MentionMenu
              candidates={candidates}
              activeIndex={activeIndex}
              onSelect={selectCandidate}
            />
          )}
          <textarea
            ref={textareaRef}
            className={styles.textarea}
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
          <Button
            type="button"
            variant="ghost"
            size="sm"
            title={expandTitle}
            aria-label="Expand editor"
            aria-keyshortcuts="Meta+e"
            onClick={() => setExpanded(true)}
            disabled={disabled}
          >
            ⤢
          </Button>
          {busy && onStop && (
            <Button
              type="button"
              variant="secondary"
              className={styles.stop}
              title="Stop generating"
              aria-label="Stop generating"
              onClick={onStop}
            >
              ■ Stop
            </Button>
          )}
          <Button variant="primary" onClick={() => void handleSend()} disabled={disabled}>
            Send
          </Button>
        </div>
      )}
    </div>
  );
}
