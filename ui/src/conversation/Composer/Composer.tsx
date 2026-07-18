import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Button } from "../../components/Button/Button";
import type { RouterClient } from "../../router/client";
import { useMentionResolver, type MentionCandidate } from "./useMentionResolver";
import { MentionMenu } from "./MentionMenu";
import { InjectedContentPreview } from "./InjectedContentPreview";
import styles from "./Composer.module.css";

export interface ComposerProps {
  routerClient: RouterClient;
  sessionId: string;
  agentId?: string;
  onSent?: (text: string, mentions: string[]) => void;
  /** Disables the textarea and Send button — set during a transport switch. */
  disabled?: boolean;
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
}

/**
 * Plain textarea, ⌘E expands in place (no modal, MADS-UX §3.3). @/ trigger
 * resolution is delegated to useMentionResolver; selecting a candidate
 * splices a plain-text reference token into the value and stages it in
 * InjectedContentPreview — nothing is fetched/expanded until send (§2.4).
 */
export function Composer({
  routerClient,
  sessionId,
  agentId,
  onSent,
  disabled,
  initialValue,
  autoSendInitial,
  onSendError,
  onAutoSendStart,
  onSendStart,
}: ComposerProps) {
  const [value, setValue] = useState(initialValue ?? "");
  const [cursor, setCursor] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const [injected, setInjected] = useState<MentionCandidate[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const { trigger, candidates } = useMentionResolver(routerClient, agentId, value, cursor);

  function selectCandidate(candidate: MentionCandidate) {
    if (!trigger) return;
    const before = value.slice(0, trigger.start);
    const after = value.slice(cursor);
    const insertion = `${candidate.insertText} `;
    const nextValue = `${before}${insertion}${after}`;
    const nextCursor = before.length + insertion.length;
    setValue(nextValue);
    setCursor(nextCursor);
    setInjected((prev) =>
      prev.some((p) => p.id === candidate.id) ? prev : [...prev, candidate],
    );
    setActiveIndex(0);
    requestAnimationFrame(() => {
      textareaRef.current?.setSelectionRange(nextCursor, nextCursor);
      textareaRef.current?.focus();
    });
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "e") {
      e.preventDefault();
      setExpanded((x) => !x);
      return;
    }

    if (trigger && candidates.length > 0) {
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
        selectCandidate(candidates[activeIndex]);
        return;
      }
    }

    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
    }
  }

  async function handleSend() {
    const text = value.trim();
    if (!text) return;
    onSendStart?.();
    const mentions = injected.map((i) => i.id);
    try {
      await routerClient.sendMessage(sessionId, { text, mentions });
      onSent?.(text, mentions);
      setValue("");
      setCursor(0);
      setInjected([]);
    } catch (err) {
      onSendError?.(err instanceof Error ? err.message : String(err));
    }
  }

  const autoSentRef = useRef(false);
  useEffect(() => {
    if (autoSendInitial && initialValue && !autoSentRef.current) {
      autoSentRef.current = true;
      onAutoSendStart?.();
      void handleSend();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- auto-send is a one-time mount effect, guarded by autoSentRef against StrictMode's double-invoke
  }, []);

  return (
    <div className={styles.composer}>
      <InjectedContentPreview
        items={injected}
        onRemove={(id) => setInjected((prev) => prev.filter((p) => p.id !== id))}
      />
      <div className={styles.inputRow}>
        {trigger && (
          <MentionMenu
            candidates={candidates}
            activeIndex={activeIndex}
            onSelect={selectCandidate}
          />
        )}
        <textarea
          ref={textareaRef}
          className={[styles.textarea, expanded ? styles.expanded : ""]
            .filter(Boolean)
            .join(" ")}
          placeholder="Message the agent — @ to mention, / for commands"
          value={value}
          disabled={disabled}
          onChange={(e) => {
            setValue(e.target.value);
            setCursor(e.target.selectionStart ?? e.target.value.length);
          }}
          onKeyUp={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
          onClick={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
          onKeyDown={handleKeyDown}
        />
        <Button variant="primary" onClick={() => void handleSend()} disabled={disabled}>
          Send
        </Button>
      </div>
    </div>
  );
}
