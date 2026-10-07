import { Suspense, lazy, useRef, type ReactNode } from "react";
import type { Monaco } from "@monaco-editor/react";
import { defineMadsMonacoTheme, MADS_MONACO_THEME } from "../../utils/createMonacoTheme";
import { bindingFor, hotkeyLabel } from "../../platform/hotkeys";
import { monacoKeybinding } from "./monacoKeybinding";
import styles from "./Composer.module.css";

// Same lazy+Suspense precedent as YamlSourceView/DiffViewer/MergeEditor —
// Monaco needs Worker/canvas APIs happy-dom doesn't provide, so tests mock
// this import instead of ever mounting the real editor.
const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

export interface ExpandedComposerProps {
  value: string;
  onChange: (value: string) => void;
  /** Sends `text` (the editor's current value): the sendFromEditor hotkey or the send button. */
  onSend: (text: string) => void;
  /** The collapseEditor hotkey (Esc) in the editor. */
  onCollapse: () => void;
  /** Character offset to place the caret at on mount — the textarea's cursor at the moment the editor expanded. Default 0. */
  initialCursor?: number;
  /** Fires on every caret move so the collapsing textarea can restore the same offset. */
  onCursorChange?: (offset: number) => void;
  disabled?: boolean;
  /** The send button's label. Default "Send"; terminal compose passes "Paste into terminal". */
  sendLabel?: string;
  /**
   * Chat semantics: Stop shows while `busy` (when `onStop` is set) and the send
   * button reads "Queue" while `busy`. Default true; terminal compose passes
   * false (the PTY has no turn to stop or queue behind).
   */
  showBusyControls?: boolean;
  busy?: boolean;
  onStop?: () => void;
  /** When set, send is disabled and this is its tooltip (e.g. attachments still uploading). */
  sendBlockedReason?: string;
  /** Rendered at the left of the action bar — the composer's `+` menu. */
  leading?: ReactNode;
  /** Appended to the action bar hint, e.g. "sent as a bracketed paste". */
  hintSuffix?: string;
}

/**
 * The line-numbered Monaco editor (the expandEditor hotkey), plus its action
 * bar: `leading` (the + menu), a `{sendFromEditor} send · Esc collapse` hint,
 * and Stop / Send / Queue. Markdown mode, word wrap, no @-mention or
 * `/`-command resolution — that trigger logic stays textarea-only.
 *
 * Reusable outside chat: the dock's terminal compose sheet renders it with
 * `sendLabel="Paste into terminal"`, `showBusyControls={false}`,
 * `hintSuffix="sent as a bracketed paste"`, and an `onSend(text)` that writes
 * formatBracketedPaste(text) to the PTY. It brings no surface of its own
 * beyond the editor's bottom hairline; the dock (or sheet) provides it.
 */
export function ExpandedComposer({
  value,
  onChange,
  onSend,
  onCollapse,
  initialCursor = 0,
  onCursorChange,
  disabled,
  sendLabel = "Send",
  showBusyControls = true,
  busy,
  onStop,
  sendBlockedReason,
  leading,
  hintSuffix,
}: ExpandedComposerProps) {
  // onMount fires exactly once per Monaco mount, not on every prop change —
  // addCommand below must dereference these every keystroke rather than
  // close over onSend/onCollapse/value directly, or the send hotkey would
  // send whatever text existed at the moment Monaco first mounted.
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;
  const onCollapseRef = useRef(onCollapse);
  onCollapseRef.current = onCollapse;
  const onCursorChangeRef = useRef(onCursorChange);
  onCursorChangeRef.current = onCursorChange;
  const valueRef = useRef(value);
  valueRef.current = value;

  const queueing = showBusyControls && Boolean(busy);
  const sendDisabled = Boolean(disabled) || Boolean(sendBlockedReason);

  return (
    <div className={styles.expanded} data-testid="expanded-composer">
      <div className={styles.editorWrap}>
        <Suspense fallback={<div className={styles.editorLoading}>Loading editor…</div>}>
          <MonacoEditor
            language="markdown"
            theme={MADS_MONACO_THEME}
            value={value}
            beforeMount={(monaco: Monaco) => defineMadsMonacoTheme(monaco)}
            onChange={(next) => onChange(next ?? "")}
            onMount={(ed, monaco) => {
              const model = ed.getModel();
              if (model) {
                const pos = model.getPositionAt(initialCursor);
                ed.setPosition(pos);
                ed.revealPositionInCenterIfOutsideViewport(pos);
              }
              ed.focus();
              // Both chords come from the hotkeys table, so a future user
              // override reaches the editor too (Monaco can only bind them
              // here, through addCommand).
              const send = monacoKeybinding(bindingFor("sendFromEditor"), monaco);
              if (send !== undefined) ed.addCommand(send, () => onSendRef.current(valueRef.current));
              const collapse = monacoKeybinding(bindingFor("collapseEditor"), monaco);
              if (collapse !== undefined) ed.addCommand(collapse, () => onCollapseRef.current());
              ed.onDidChangeCursorPosition((e) => {
                const m = ed.getModel();
                onCursorChangeRef.current?.(m ? m.getOffsetAt(e.position) : 0);
              });
            }}
            options={{
              wordWrap: "on",
              minimap: { enabled: false },
              lineNumbers: "on",
              lineNumbersMinChars: 3,
              // A gap between the numbers and the text (Monaco's default is
              // tight once folding is off) and the mockup's 8px top inset.
              lineDecorationsWidth: 14,
              padding: { top: 8 },
              glyphMargin: false,
              folding: false,
              readOnly: disabled,
              fontSize: 13,
              scrollBeyondLastLine: false,
              automaticLayout: true,
            }}
          />
        </Suspense>
      </div>
      <div className={styles.exbar}>
        {leading}
        <span className={styles.hint}>
          <kbd className={styles.kbd}>{hotkeyLabel("sendFromEditor")}</kbd> send ·{" "}
          <kbd className={styles.kbd}>{hotkeyLabel("collapseEditor")}</kbd> collapse
          {hintSuffix ? ` · ${hintSuffix}` : ""}
        </span>
        <span className={styles.spacer} />
        {queueing && onStop && (
          <button
            type="button"
            className={styles.stop}
            title="Stop generating"
            aria-label="Stop generating"
            onClick={onStop}
          >
            ■ Stop
          </button>
        )}
        <button
          type="button"
          className={[styles.send, queueing && styles.queue].filter(Boolean).join(" ")}
          onClick={() => onSend(value)}
          disabled={sendDisabled}
          title={
            sendBlockedReason ??
            (queueing ? "Queue — sends when the current turn finishes" : undefined)
          }
        >
          {queueing ? "Queue" : sendLabel}
        </button>
      </div>
    </div>
  );
}
