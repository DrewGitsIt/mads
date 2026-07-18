import { Suspense, lazy, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import type { FileDiff } from "../../router/types";
import styles from "./DiffViewer.module.css";

// Lazy-loaded so Monaco (a heavy dep, first introduction in this codebase —
// HANDOFF §5/§11) never lands in the initial bundle and never has to mount
// under happy-dom/jsdom in tests, which mock this import instead (see
// DiffViewer.test.tsx).
const MonacoDiffEditor = lazy(() =>
  import("@monaco-editor/react").then((m) => ({ default: m.DiffEditor })),
);

const LANGUAGE_BY_EXT: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  css: "css",
  json: "json",
  md: "markdown",
  py: "python",
  yml: "yaml",
  yaml: "yaml",
  sh: "shell",
};

/** Best-effort Monaco language id from a path extension — wrong is fine, it only affects syntax highlighting. */
function guessLanguage(path: string): string {
  const ext = path.split(".").pop() ?? "";
  return LANGUAGE_BY_EXT[ext] ?? "plaintext";
}

export interface DiffViewerProps {
  /** The selected file's path — kept even when `diff` fails to load, since "Open in editor" stays visible either way (MADS-UX §3.3). */
  path?: string;
  /** Short hash of the commit this diff is from, when the selection is a committed file rather than a working change. */
  commitHash?: string;
  diff: FileDiff | null;
  loading?: boolean;
  error?: string;
  onOpenInEditor: (path: string) => void;
}

/** Side-by-side/inline Monaco diff viewer, plus the always-visible "Open in editor" action. */
export function DiffViewer({ path, commitHash, diff, loading, error, onOpenInEditor }: DiffViewerProps) {
  const [sideBySide, setSideBySide] = useState(true);

  return (
    <div className={styles.viewer}>
      <div className={styles.toolbar}>
        <Chip tone="neutral" pressed={sideBySide} onClick={() => setSideBySide(true)}>
          Side by side
        </Chip>
        <Chip tone="neutral" pressed={!sideBySide} onClick={() => setSideBySide(false)}>
          Inline
        </Chip>
        {commitHash && <Chip tone="neutral">Diff from commit {commitHash.slice(0, 7)}</Chip>}
        <div className={styles.spacer} />
        <Button
          variant="secondary"
          size="sm"
          disabled={!path}
          onClick={() => path && onOpenInEditor(path)}
        >
          Open in editor
        </Button>
      </div>

      {loading && <div className={styles.status}>Loading diff…</div>}
      {!loading && error && <div className={styles.status}>{error}</div>}
      {!loading && !error && !diff && (
        <div className={styles.status}>Select a file to view its diff.</div>
      )}
      {!loading && !error && diff && (
        <Suspense fallback={<div className={styles.status}>Loading editor…</div>}>
          <MonacoDiffEditor
            height="480px"
            theme="vs-dark"
            language={diff.language ?? guessLanguage(diff.path)}
            original={diff.original}
            modified={diff.modified}
            options={{ renderSideBySide: sideBySide, readOnly: true }}
          />
        </Suspense>
      )}
    </div>
  );
}
