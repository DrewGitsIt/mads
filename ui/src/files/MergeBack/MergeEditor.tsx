import { Suspense, lazy, useState } from "react";
import { Button } from "../../components/Button/Button";
import styles from "./MergeEditor.module.css";

// Same lazy-load discipline as DiffViewer (HANDOFF §5) — `Editor` is
// @monaco-editor/react's plain single-pane editor (not DiffEditor), used
// here over raw conflict-marker text rather than a purpose-built 3-way UI:
// the standalone monaco-editor distribution doesn't ship VS Code's built-in
// merge editor.
const MonacoEditor = lazy(() =>
  import("@monaco-editor/react").then((m) => ({ default: m.default })),
);

export interface MergeEditorProps {
  path: string;
  conflictText: string;
  onResolve: (text: string) => void;
}

/**
 * Seed-only embedded merge editor stub (HANDOFF §5). Nothing in the current
 * backend leaves real conflict-marker content in the tree to point this
 * at — `Manager.Merge` aborts the merge on conflict — so this component is
 * only ever exercised against a fixture string, never real content. Wiring
 * it to real content needs a W4.5 follow-up (leave conflict markers in the
 * worktree, or add a conflict-content endpoint).
 */
export function MergeEditor({ path, conflictText, onResolve }: MergeEditorProps) {
  const [text, setText] = useState(conflictText);
  const [resolved, setResolved] = useState(false);

  return (
    <div className={styles.editor}>
      <div className={styles.note}>
        Preview only — {path} has no real conflict content to load yet (see
        ui/src/files/HANDOFF.md §5).
      </div>
      <Suspense fallback={<div className={styles.loading}>Loading editor…</div>}>
        <MonacoEditor
          height="240px"
          theme="vs-dark"
          language="plaintext"
          value={text}
          onChange={(value) => setText(value ?? "")}
        />
      </Suspense>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => {
          setResolved(true);
          onResolve(text);
        }}
      >
        Mark resolved (preview only)
      </Button>
      {resolved && <div className={styles.resolvedNote}>Marked resolved in this preview.</div>}
    </div>
  );
}
