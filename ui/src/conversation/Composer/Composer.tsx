import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import type { RouterClient } from "../../router/client";
import { useCompactionSupport } from "../../router/compaction";
import type { Link, QueuedMessage, Session, Usage } from "../../router/types";
import {
  commandCandidates,
  sameTrigger,
  useMentionResolver,
  type MentionCandidate,
} from "./useMentionResolver";
import { MentionMenu, mentionOptionId } from "./MentionMenu";
import { CommandCardHost, CommandNotice } from "./CommandCardHost";
import {
  COMMANDS,
  availability,
  findCommand,
  parseCommandLine,
} from "./commands/registry";
import type {
  CommandApi,
  CommandCardRender,
  CommandContext,
  CommandDescriptor,
  CommandInvocation,
  CommandToastAction,
} from "./commands/types";
import { useLinkCommandState } from "./commands/useLinkCommandState";
import { InjectedContentPreview } from "./InjectedContentPreview";
import { ExpandedComposer } from "./ExpandedComposer";
import { ComposerPlusMenu, type ComposerMode } from "./ComposerPlusMenu";
import { getDraft, setDraft } from "./composerDrafts";
import { AttachmentChips } from "../../components/Attachments/AttachmentChips";
import { stagedForSession } from "../../components/Attachments/stagedAttachments";
import { useStagedAttachments } from "../../components/Attachments/useStagedAttachments";
import { useFileDrop } from "../../components/Attachments/useFileDrop";
import { appendAttachments } from "../attachmentRefs";
import { QueuedMessages } from "./QueuedMessages";
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
   * Applies a "/model" command (the same commit path the dock's model
   * picker uses). Resolves true when the change was accepted
   * but queued behind an in-flight turn — a model switch never interrupts a
   * running agent, so the confirmation has to say "applies when the current
   * turn finishes" rather than claiming it already took effect. Rejects on
   * failure, surfaced through onCommandError.
   */
  onUpdateSession?: (
    patch: Partial<Pick<Session, "model">>,
  ) => void | Promise<boolean | void>;
  /** Fires once a "/" command has done its thing — surface as a toast (with `action` as its link), not a transcript message. */
  onCommandExecuted?: (message: string, action?: CommandToastAction) => void;
  /** Fires when a "/" command fails (e.g. onUpdateSession or the Router rejects it). */
  onCommandError?: (message: string) => void;
  /** Opens one of this session's tasks — "/task"'s toast link. Absent → no link. */
  onOpenTask?: (taskId: string) => void;
  /** "/compact [focus]" — compacts through the same path as the Compact-now button. */
  onCompactSession?: (instructions?: string) => void;
  /** This session's record — the /new card prefills its pills from it. */
  session?: Session;
  /** Links touching this session — /new-leader is unavailable while one leads it. */
  links?: Link[];
  /** The "/" command registry. Default COMMANDS; tests pass their own. */
  commands?: CommandDescriptor[];
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
 * `#` skills insert their name as text the same way but never stage a chip.
 * `/` resolves only the command registry (commands/registry.ts): a command
 * with a submenu ("/model") descends to a second level, one that
 * needs args (/task) inserts "/name " to type them, and the rest run on
 * Enter or click. Tab only completes "/name ", so optional args can follow.
 * Sending a line that starts with a registered "/name" runs that command
 * instead of sending text; an unknown "/word" still goes to the agent.
 * Commands never write to the transcript — their results are toasts or a
 * card in the CommandCardHost above the input.
 */
/** The row a fresh list selects: the first enabled one, else the first. */
function firstSelectable(rows: MentionCandidate[]): number {
  return Math.max(
    0,
    rows.findIndex((c) => c.disabledReason === undefined),
  );
}

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
  onOpenTask,
  onCompactSession,
  session,
  links,
  commands = COMMANDS,
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
  const { dragging, dropHandlers, onPaste } = useFileDrop((files) =>
    staged.add(files),
  );

  // What commands see (available(), options()). Memoized so the resolver's
  // effect re-runs only when one of these actually changes.
  const compaction = useCompactionSupport(provider);
  const { linkedSessions, leader, pipelineRunning } = useLinkCommandState(
    routerClient,
    sessionId,
    links,
  );
  // The session record is replaced on every update (a turn ending, activity),
  // which would re-run the resolver and reset the menu's selection. Commands
  // read only these fields of it, so the ctx keeps one record until one of
  // them changes; execute still gets the latest (commandApi below).
  const sessionKey =
    session &&
    JSON.stringify([
      session.id,
      session.name,
      session.dir,
      session.workspace,
      session.agent,
      session.provider,
      session.model,
      session.trust,
      session.isolation,
    ]);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the fields commands read, see above
  const commandSession = useMemo(() => session, [sessionKey]);
  const commandCtx = useMemo<CommandContext>(
    () => ({
      routerClient,
      sessionId,
      agentId,
      provider,
      compaction,
      session: commandSession,
      linkedSessions,
      leader,
      pipelineRunning,
    }),
    [
      routerClient,
      sessionId,
      agentId,
      provider,
      compaction,
      commandSession,
      linkedSessions,
      leader,
      pipelineRunning,
    ],
  );
  const { trigger, candidates, candidatesFor } = useMentionResolver(
    commandCtx,
    value,
    cursor,
    undefined,
    commands,
  );
  const menuOpen =
    !expanded && Boolean(trigger) && !menuDismissed && candidates.length > 0;
  // A narrowing filter can shrink candidates out from under a higher
  // activeIndex (e.g. arrowed to #4, then typed a letter that leaves 2
  // matches) — reset whenever the rows change so selection/aria-
  // activedescendant never index past the end. Lands on the first selectable
  // row. A re-resolve to the same rows (a ctx refresh) keeps the selection.
  const rowsKey = candidates
    .map((c) => `${c.id}\u0000${c.disabledReason ?? ""}`)
    .join("\n");
  const [activeRowsKey, setActiveRowsKey] = useState(rowsKey);
  if (activeRowsKey !== rowsKey) {
    setActiveRowsKey(rowsKey);
    setActiveIndex(firstSelectable(candidates));
  }

  // The card above the input (CommandCardHost). A `notice` is a command's
  // inline refusal ("/task needs a title") and clears on the next edit.
  const [card, setCard] = useState<{
    render: CommandCardRender;
    notice?: boolean;
  }>();

  const closeCard = useCallback(() => {
    setCard(undefined);
    requestAnimationFrame(() => textareaRef.current?.focus());
  }, []);

  function showNotice(message: string) {
    setCard({
      render: () => <CommandNotice message={message} />,
      notice: true,
    });
    setMenuDismissed(true);
  }

  function focusTextareaAt(offset: number) {
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(offset, offset);
    });
  }

  // Arrow keys step over disabled (unavailable) command rows.
  function stepActive(dir: 1 | -1) {
    const n = candidates.length;
    setActiveIndex((i) => {
      for (let step = 1; step <= n; step++) {
        const j = (((i + dir * step) % n) + n) % n;
        if (candidates[j].disabledReason === undefined) return j;
      }
      return i;
    });
  }

  // "accept" (Enter, click) runs a command row when it can run as-is;
  // "complete" (Tab) only writes "/name " so args can follow — then Enter
  // sends the line, args and all.
  function selectCandidate(
    candidate: MentionCandidate,
    mode: "accept" | "complete" = "accept",
  ) {
    if (!trigger) return;
    if (
      mode === "accept" &&
      (candidate.kind === "command" || candidate.kind === "command-option")
    ) {
      const command =
        candidate.command &&
        findCommand(candidate.command, commandCtx, commands);
      if (!command) return;
      const range = { start: trigger.start, end: cursor };
      if (candidate.kind === "command-option") {
        const { id, label, detail } = candidate;
        runCommand(
          command,
          { args: label, option: { id, label, detail } },
          range,
        );
        return;
      }
      if (candidate.disabledReason !== undefined) {
        // Only reachable by keyboard, when every row left is disabled — the
        // user typed it. A command with its own blocked state shows that.
        if (command.runsWhenUnavailable) runCommand(command, { args: "" }, range);
        else showNotice(candidate.disabledReason);
        return;
      }
      if (!command.options && !command.argRequired) {
        runCommand(command, { args: "" }, range);
        return;
      }
      // Otherwise insert "/name " below: that descends to the submenu, or
      // leaves the caret where the required args go.
    }
    const before = value.slice(0, trigger.start);
    const after = value.slice(cursor);
    const insertion = `${candidate.insertText} `;
    const nextValue = `${before}${insertion}${after}`;
    const nextCursor = before.length + insertion.length;
    setValue(nextValue);
    setCursor(nextCursor);
    // Only @-mentions/attachments stage into the chip strip — a skill is
    // plain text for the agent and a command never leaves a chip behind.
    if (candidate.kind === "mentionable") {
      setInjected((prev) =>
        prev.some((p) => p.id === candidate.id) ? prev : [...prev, candidate],
      );
    }
    setActiveIndex(0);
    focusTextareaAt(nextCursor);
  }

  const commandApi: CommandApi = {
    ctx: { ...commandCtx, session },
    updateSession: async (patch) => onUpdateSession?.(patch),
    notify: (message, action) => onCommandExecuted?.(message, action),
    fail: (message) => onCommandError?.(message),
    openCard: (render) => setCard({ render }),
    openTask: onOpenTask,
    compact: onCompactSession,
  };

  // Runs a command against the session, wiping its text (value[range]) out
  // of the composer — never leaves residual command text or a chip behind.
  // An unavailable command, or one missing its required args, shows why
  // inline instead and keeps the text so it can be fixed.
  function runCommand(
    command: CommandDescriptor,
    invocation: CommandInvocation,
    range: { start: number; end: number },
  ) {
    const avail = availability(command, commandCtx);
    if (!avail.ok && !command.runsWhenUnavailable) {
      showNotice(avail.reason);
      return;
    }
    const argError = command.argError?.(invocation.args, commandCtx);
    if (argError) {
      showNotice(argError);
      return;
    }
    if (command.argRequired && !invocation.args && !invocation.option) {
      showNotice(command.argRequired);
      return;
    }
    const before = value.slice(0, range.start);
    setValue(`${before}${value.slice(range.end)}`);
    setCursor(before.length);
    setActiveIndex(0);
    setCard(undefined);
    focusTextareaAt(before.length);
    void command.execute(invocation, commandApi);
  }

  // Enter, Send, and the expanded editor's send: a message that starts with
  // a registered "/name" runs that command rather than going to the agent.
  // A bare submenu command ("/model" after Esc closed the menu) reopens its
  // submenu instead. Unknown "/words" are sent as text, as before.
  function submit() {
    const parsed = parseCommandLine(value);
    const command = parsed && findCommand(parsed.name, commandCtx, commands);
    if (!parsed || !command) {
      void handleSend();
      return;
    }
    if (
      command.options &&
      !parsed.args &&
      availability(command, commandCtx).ok
    ) {
      const nextValue = `/${command.name} `;
      setExpanded(false);
      setValue(nextValue);
      setCursor(nextValue.length);
      setMenuDismissed(false);
      focusTextareaAt(nextValue.length);
      return;
    }
    runCommand(command, { args: parsed.args }, { start: 0, end: value.length });
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
    if (card && e.key === "Escape") {
      e.preventDefault();
      closeCard();
      return;
    }

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
        stepActive(1);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        stepActive(-1);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        const mode = e.key === "Tab" ? "complete" : "accept";
        if (sameTrigger(candidatesFor, trigger)) {
          const candidate = candidates[activeIndex];
          if (candidate) selectCandidate(candidate, mode);
          return;
        }
        // The rows lag the text by the resolver's debounce: "/c", a pause,
        // then "ontext" + Enter must not pick /compact off the "/c" list. An
        // exact name or alias acts on what's typed now; anything else waits
        // for its rows.
        const exact =
          trigger?.type === "/" &&
          !trigger.query.includes(" ") &&
          findCommand(trigger.query, commandCtx, commands);
        const row =
          exact &&
          commandCandidates(trigger.query, commandCtx, commands).find(
            (c) => c.command === exact.name,
          );
        if (row) selectCandidate(row, mode);
        return;
      }
    }

    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
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
  const sendBlockedReason = uploading
    ? "Waiting for attachments to finish uploading"
    : undefined;

  return (
    <div
      className={[styles.composer, dragging && styles.dropTarget]
        .filter(Boolean)
        .join(" ")}
      data-composer=""
      onKeyDownCapture={handleComposerKeyDownCapture}
      onPaste={onPaste}
      {...dropHandlers}
    >
      {card && (
        <CommandCardHost onClose={closeCard}>
          {card.render(closeCard)}
        </CommandCardHost>
      )}
      <QueuedMessages queued={queued} onCancel={onCancelQueued} />
      <div className={styles.chips}>
        <InjectedContentPreview
          items={injected}
          onRemove={(id) =>
            setInjected((prev) => prev.filter((p) => p.id !== id))
          }
        />
        <AttachmentChips
          items={attachments}
          thumbnailUrl={(a) =>
            a.attachment?.thumbnail
              ? routerClient.attachmentUrl(
                  staged.owner,
                  a.attachment.id,
                  "thumbnail",
                )
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
          onChange={(next) => {
            setValue(next);
            if (card?.notice) setCard(undefined);
          }}
          onSend={submit}
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
              if (card?.notice) setCard(undefined);
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
            className={[styles.send, busy && styles.queue]
              .filter(Boolean)
              .join(" ")}
            onClick={submit}
            disabled={disabled || uploading}
            title={
              sendBlockedReason ??
              (busy
                ? "Queue — sends when the current turn finishes"
                : undefined)
            }
          >
            {busy ? "Queue" : "Send"}
          </button>
        </div>
      )}
    </div>
  );
}
