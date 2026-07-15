import { useRef, useState, type KeyboardEvent } from "react";
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
}

/**
 * Plain textarea, ⌘E expands in place (no modal, MADS-UX §3.3). @/ trigger
 * resolution is delegated to useMentionResolver; selecting a candidate
 * splices a plain-text reference token into the value and stages it in
 * InjectedContentPreview — nothing is fetched/expanded until send (§2.4).
 */
export function Composer({ routerClient, sessionId, agentId, onSent }: ComposerProps) {
  const [value, setValue] = useState("");
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
      handleSend();
    }
  }

  function handleSend() {
    const text = value.trim();
    if (!text) return;
    const mentions = injected.map((i) => i.id);
    void routerClient.sendMessage(sessionId, { text, mentions });
    onSent?.(text, mentions);
    setValue("");
    setCursor(0);
    setInjected([]);
  }

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
          onChange={(e) => {
            setValue(e.target.value);
            setCursor(e.target.selectionStart ?? e.target.value.length);
          }}
          onKeyUp={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
          onClick={(e) => setCursor(e.currentTarget.selectionStart ?? 0)}
          onKeyDown={handleKeyDown}
        />
        <Button variant="primary" onClick={handleSend}>
          Send
        </Button>
      </div>
    </div>
  );
}
