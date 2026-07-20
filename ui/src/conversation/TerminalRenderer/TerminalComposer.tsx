import { Suspense, lazy, useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { Monaco } from "@monaco-editor/react";
import type { editor } from "monaco-editor";
import { Button } from "../../components/Button/Button";
import { defineMadsMonacoTheme, MADS_MONACO_THEME } from "../../utils/createMonacoTheme";
import styles from "./TerminalComposer.module.css";

// Same lazy+Suspense precedent as ExpandedComposer/YamlSourceView/DiffViewer.
const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

/** Standard xterm/bash bracketed-paste convention — see HANDOFF.md "PTY bracketed paste format". */
export function formatBracketedPaste(text: string): string {
  return `\x1b[200~${text}\x1b[201~`;
}

export interface TerminalComposerProps {
  open: boolean;
  onClose: () => void;
  /** Called with the raw (unformatted) text on Send — the caller owns bracketed-paste formatting and the actual PTY write (see ConversationTab). */
  onSend: (text: string) => void;
}

/**
 * ⌘E overlay above the live terminal (deliberate deviation from MADS-UX
 * §3.3's "no modal" rule — terminal mode has no in-place expansion target
 * the way chat's textarea does). Plain-text markdown compose only, same as
 * ExpandedComposer: no @-mention/`/`-command resolution here (HANDOFF.md
 * "Edge Cases & TODOs").
 */
export function TerminalComposer({ open, onClose, onSend }: TerminalComposerProps) {
  const [value, setValue] = useState("");
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    if (open) setValue("");
  }, [open]);

  // The overlay covers the terminal but doesn't otherwise stop page scroll —
  // pin the body so the terminal never scrolls out from under it while open.
  useEffect(() => {
    if (!open) return;
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = prevOverflow;
    };
  }, [open]);

  if (!open) return null;

  function handleSend() {
    onSendRef.current(valueRef.current);
  }

  // Escape must close exactly once whether focus is on Monaco or on the
  // Cancel/Send buttons. A capture-phase handler here fires before Monaco's
  // own internal keybinding service ever sees the native keydown (same
  // mechanism Composer.tsx uses for ⌘E) — stopping propagation means Monaco
  // never gets a chance to *also* run an addCommand(Escape) handler for the
  // same keystroke, so there's exactly one owner of Escape, not two.
  function handleKeyDownCapture(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onCloseRef.current();
    }
  }

  return (
    <div className={styles.backdrop} onKeyDownCapture={handleKeyDownCapture}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Compose terminal input"
        className={styles.panel}
      >
        <div className={styles.editorWrap} data-testid="terminal-composer">
          <Suspense fallback={<div className={styles.editorLoading}>Loading editor…</div>}>
            <MonacoEditor
              language="markdown"
              theme={MADS_MONACO_THEME}
              value={value}
              beforeMount={(monaco: Monaco) => defineMadsMonacoTheme(monaco)}
              onChange={(next) => setValue(next ?? "")}
              onMount={(ed: editor.IStandaloneCodeEditor, monaco: Monaco) => {
                ed.focus();
                ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, () => handleSend());
              }}
              options={{
                wordWrap: "on",
                minimap: { enabled: false },
                lineNumbers: "off",
                fontSize: 13,
                scrollBeyondLastLine: false,
                automaticLayout: true,
              }}
            />
          </Suspense>
        </div>
        <div className={styles.actions}>
          <Button type="button" variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button type="button" variant="primary" size="sm" onClick={handleSend}>
            Send
          </Button>
        </div>
      </div>
    </div>
  );
}
