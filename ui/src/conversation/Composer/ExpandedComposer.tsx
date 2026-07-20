import { Suspense, lazy, useRef } from "react";
import type { Monaco } from "@monaco-editor/react";
import type { editor } from "monaco-editor";
import { defineMadsMonacoTheme, MADS_MONACO_THEME } from "../../utils/createMonacoTheme";
import styles from "./Composer.module.css";

// Same lazy+Suspense precedent as YamlSourceView/DiffViewer/MergeEditor —
// Monaco needs Worker/canvas APIs happy-dom doesn't provide, so tests mock
// this import instead of ever mounting the real editor.
const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

export interface ExpandedComposerProps {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  onCollapse: () => void;
  /** Character offset to place the caret at on mount — the textarea's cursor at the moment ⌘E expanded. */
  initialCursor: number;
  /** Fires on every caret move so the collapsing textarea can restore the same offset. */
  onCursorChange: (offset: number) => void;
  disabled?: boolean;
}

/**
 * Monaco stand-in for the plain textarea while expanded (⌘E). Markdown mode,
 * word wrap, no @-mention/`/`-command resolution — that trigger logic stays
 * textarea-only for this increment (HANDOFF.md "Edge Cases & TODOs").
 */
export function ExpandedComposer({
  value,
  onChange,
  onSend,
  onCollapse,
  initialCursor,
  onCursorChange,
  disabled,
}: ExpandedComposerProps) {
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  // onMount fires exactly once per Monaco mount, not on every prop change —
  // addCommand below must dereference these every keystroke rather than
  // close over onSend/onCollapse directly, or ⌘Enter would send whatever
  // text existed at the moment Monaco first mounted instead of the latest.
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;
  const onCollapseRef = useRef(onCollapse);
  onCollapseRef.current = onCollapse;

  return (
    <div className={styles.editorWrap} data-testid="expanded-composer">
      <Suspense fallback={<div className={styles.editorLoading}>Loading editor…</div>}>
        <MonacoEditor
          language="markdown"
          theme={MADS_MONACO_THEME}
          value={value}
          beforeMount={(monaco: Monaco) => defineMadsMonacoTheme(monaco)}
          onChange={(next) => onChange(next ?? "")}
          onMount={(ed, monaco) => {
            editorRef.current = ed;
            const model = ed.getModel();
            if (model) {
              const pos = model.getPositionAt(initialCursor);
              ed.setPosition(pos);
              ed.revealPositionInCenterIfOutsideViewport(pos);
            }
            ed.focus();
            ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, () => onSendRef.current());
            ed.addCommand(monaco.KeyCode.Escape, () => onCollapseRef.current());
            ed.onDidChangeCursorPosition((e) => {
              const m = ed.getModel();
              onCursorChange(m ? m.getOffsetAt(e.position) : 0);
            });
          }}
          options={{
            wordWrap: "on",
            minimap: { enabled: false },
            lineNumbers: "off",
            readOnly: disabled,
            fontSize: 13,
            scrollBeyondLastLine: false,
            automaticLayout: true,
          }}
        />
      </Suspense>
    </div>
  );
}
