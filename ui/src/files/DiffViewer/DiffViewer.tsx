import { Suspense, lazy, useEffect, useRef, useState, type ReactNode } from "react";
import type { DiffOnMount, Monaco } from "@monaco-editor/react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import type { FileDiff } from "../../router/types";
import { MADS_MONACO_THEME, defineMadsMonacoTheme } from "../../utils/createMonacoTheme";
import styles from "./DiffViewer.module.css";

// Lazy-loaded so Monaco (a heavy dep, first introduction in this codebase —
// HANDOFF §5/§11) never lands in the initial bundle and never has to mount
// under happy-dom/jsdom in tests, which mock this import instead (see
// DiffViewer.test.tsx).
const MonacoDiffEditor = lazy(() =>
  import("@monaco-editor/react").then((m) => ({ default: m.DiffEditor })),
);
const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

/** localStorage key: the last Inline / Side by side choice. A view preference, so local. */
export const SIDE_BY_SIDE_KEY = "mads.diff.sideBySide";

function readSideBySide(): boolean {
  try {
    return localStorage.getItem(SIDE_BY_SIDE_KEY) === "1";
  } catch {
    return false;
  }
}

/**
 * Options shared by both editors: wrap long lines instead of scrolling
 * sideways, and spend no width on a minimap, overview ruler, folding or
 * glyph margin — on a 13" laptop the diff pane is the scarce resource.
 */
const READ_ONLY_OPTIONS = {
  readOnly: true,
  // automaticLayout re-measures on pane resize — without it Monaco leaves stale canvas blocks when the flex column changes size.
  automaticLayout: true,
  wordWrap: "on",
  minimap: { enabled: false },
  overviewRulerLanes: 0,
  hideCursorInOverviewRuler: true,
  scrollBeyondLastLine: false,
  glyphMargin: false,
  folding: false,
  lineNumbersMinChars: 3,
  lineDecorationsWidth: 8,
  renderLineHighlight: "none",
  scrollbar: { verticalScrollbarSize: 10, horizontalScrollbarSize: 10 },
} as const;

/** What a diff is: an ordinary change, or a whole file added or deleted (nothing to compare). */
function diffShape(diff: FileDiff): "added" | "deleted" | "changed" {
  if (diff.original === "" && diff.modified !== "") return "added";
  if (diff.modified === "" && diff.original !== "") return "deleted";
  return "changed";
}

type MonacoDiffInstance = Parameters<NonNullable<DiffOnMount>>[0];

/**
 * Monaco decides the original side's word wrap once, from the view it was
 * created in (both overrides "off" for inline), so switching Inline → Side
 * by side left that side unwrapped.
 * Set it to match the view (inline hides that side; wrapping it is waste).
 */
function wrapOriginalSide(editor: MonacoDiffInstance, sideBySide: boolean) {
  // Override2 outranks override1, and Monaco pinned it "off" for inline.
  editor.getOriginalEditor().updateOptions({ wordWrapOverride1: sideBySide ? "on" : "off", wordWrapOverride2: "inherit" });
}

function lineCount(text: string): number {
  if (!text) return 0;
  return text.split("\n").length - (text.endsWith("\n") ? 1 : 0);
}

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
  /** Repo-relative display form of `path` (see ../displayPath) — the toolbar label; `path` stays the title/editor target. */
  pathLabel?: string;
  /** Short hash of the commit this diff is from, when the selection is a committed file rather than a working change. */
  commitHash?: string;
  diff: FileDiff | null;
  loading?: boolean;
  error?: string;
  onOpenInEditor: (path: string) => void;
  /**
   * Replaces the path label with a richer header (the Changes tab's badge,
   * path, counts, commit link and turn chips); the view toggles and "Open in
   * editor" stay at the end of the row.
   */
  header?: ReactNode;
  /** Shown instead of the "Select a file" prompt when there's no diff to load. */
  emptyMessage?: string;
}

/** Side-by-side/inline Monaco diff viewer, plus the always-visible "Open in editor" action. */
export function DiffViewer({
  path,
  pathLabel,
  commitHash,
  diff,
  loading,
  error,
  onOpenInEditor,
  header,
  emptyMessage,
}: DiffViewerProps) {
  // Inline by default: it's the one that reads at any width.
  const [sideBySide, setSideBySideState] = useState(readSideBySide);
  const setSideBySide = (v: boolean) => {
    setSideBySideState(v);
    try {
      localStorage.setItem(SIDE_BY_SIDE_KEY, v ? "1" : "0");
    } catch {
      // Private mode / storage full: the toggle still works for this view.
    }
  };
  const showToggle = !diff || diffShape(diff) === "changed";

  return (
    <div className={styles.viewer}>
      <div className={styles.toolbar} data-has-header={header !== undefined || undefined}>
        {header !== undefined ? (
          <span className={styles.header}>{header}</span>
        ) : path ? (
          // Start-side (RTL) truncation: the filename tail is the identifying
          // part. The span doubles as the toolbar's flexible spacer.
          <span className={styles.pathLabel} title={path}>
            <span className={styles.pathText}>{pathLabel ?? path}</span>
          </span>
        ) : (
          <div className={styles.spacer} />
        )}
        <span className={styles.tools}>
          {showToggle && (
            <>
              <Chip tone="neutral" pressed={!sideBySide} onClick={() => setSideBySide(false)}>
                Inline
              </Chip>
              <Chip tone="neutral" pressed={sideBySide} onClick={() => setSideBySide(true)}>
                Side by side
              </Chip>
            </>
          )}
          {commitHash && <Chip tone="neutral">Diff from commit {commitHash.slice(0, 7)}</Chip>}
          <Button variant="secondary" size="sm" disabled={!path} onClick={() => path && onOpenInEditor(path)}>
            Open in editor
          </Button>
        </span>
      </div>

      <DiffBody diff={diff} loading={loading} error={error} sideBySide={sideBySide} emptyMessage={emptyMessage} />
    </div>
  );
}

/**
 * Keeps the "mads" Monaco theme in step with the palette: defined before the
 * first mount, redefined when Settings changes [data-theme]/[data-palette].
 */
function useMadsTheme() {
  const monacoRef = useRef<Monaco | null>(null);
  useEffect(() => {
    if (typeof MutationObserver === "undefined") return;
    const mo = new MutationObserver(() => {
      const monaco = monacoRef.current;
      if (!monaco) return;
      defineMadsMonacoTheme(monaco);
      monaco.editor.setTheme(MADS_MONACO_THEME);
    });
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "data-palette"] });
    return () => mo.disconnect();
  }, []);
  return (monaco: Monaco) => {
    monacoRef.current = monaco;
    defineMadsMonacoTheme(monaco);
  };
}

function DiffBody({
  diff,
  loading,
  error,
  sideBySide,
  emptyMessage,
}: {
  diff: FileDiff | null;
  loading?: boolean;
  error?: string;
  sideBySide: boolean;
  emptyMessage?: string;
}) {
  const beforeMount = useMadsTheme();
  const diffEditorRef = useRef<MonacoDiffInstance | null>(null);
  useEffect(() => {
    if (diffEditorRef.current) wrapOriginalSide(diffEditorRef.current, sideBySide);
  }, [sideBySide]);
  const shape = diff ? diffShape(diff) : "changed";
  const language = diff ? (diff.language ?? guessLanguage(diff.path)) : undefined;
  const plain = shape !== "changed";
  const plainText = !diff ? "" : shape === "added" ? diff.modified : diff.original;
  return (
    <>
      {loading && <div className={styles.status}>Loading diff…</div>}
      {!loading && error && <div className={styles.status}>{error}</div>}
      {!loading && !error && !diff && (
        <div className={styles.status}>{emptyMessage ?? "Select a file to view its diff."}</div>
      )}
      {!loading && !error && diff && (
        <Suspense fallback={<div className={styles.status}>Loading editor…</div>}>
          {plain && (
            // A whole file added or deleted has nothing to compare against: show
            // it as plain code with a thin edge bar, not wall-to-wall diff color.
            <>
              <div className={styles.banner}>
                {shape === "added" ? "New file" : "Deleted file"} · {lineCount(plainText)} lines
              </div>
              <div className={styles.editorWrap} data-shape={shape}>
                <MonacoEditor
                  height="100%"
                  theme={MADS_MONACO_THEME}
                  beforeMount={beforeMount}
                  language={language}
                  value={plainText}
                  options={READ_ONLY_OPTIONS}
                />
              </div>
            </>
          )}
          {/* Stays mounted (hidden, emptied) while a plain file shows: unmounting
              it as the plain editor mounts makes @monaco-editor/react dispose
              its models before the widget lets go ("TextModel got disposed
              before DiffEditorWidget model got reset"). */}
          <div className={styles.editorWrap} hidden={plain}>
            <MonacoDiffEditor
              height="100%"
              onMount={(editor) => {
                diffEditorRef.current = editor;
                wrapOriginalSide(editor, sideBySide);
              }}
              theme={MADS_MONACO_THEME}
              beforeMount={beforeMount}
              language={language}
              original={plain ? "" : diff.original}
              modified={plain ? "" : diff.modified}
              options={{
                ...READ_ONLY_OPTIONS,
                renderSideBySide: sideBySide,
                // Monaco silently forces inline below 900px; the toggle is the user's call.
                useInlineViewWhenSpaceIsLimited: false,
                diffWordWrap: "on",
                renderOverviewRuler: false,
                renderMarginRevertIcon: false,
                // Long files fold their unchanged stretches, with 3 lines of context.
                hideUnchangedRegions: { enabled: true, contextLineCount: 3, minimumLineCount: 5, revealLineCount: 20 },
              }}
            />
          </div>
        </Suspense>
      )}
    </>
  );
}
