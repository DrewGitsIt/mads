import { Suspense, lazy } from "react";
import styles from "./YamlSourceView.module.css";

// Lazy-loaded so Monaco never lands in the initial bundle and never has to
// mount under happy-dom/jsdom in tests, which mock this import instead —
// same precedent as ui/src/files/DiffViewer/DiffViewer.tsx.
const MonacoEditor = lazy(() => import("@monaco-editor/react").then((m) => ({ default: m.Editor })));

export interface YamlSourceViewProps {
  source: string;
  onChange: (source: string) => void;
}

/** Monaco instance bound directly to the template's `source` — the YAML-mode view. */
export function YamlSourceView({ source, onChange }: YamlSourceViewProps) {
  return (
    <div className={styles.view}>
      <Suspense fallback={<div className={styles.status}>Loading editor…</div>}>
        <MonacoEditor
          height="480px"
          theme="vs-dark"
          language="yaml"
          value={source}
          onChange={(value) => onChange(value ?? "")}
        />
      </Suspense>
    </div>
  );
}
