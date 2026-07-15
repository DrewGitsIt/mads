import { useState } from "react";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { Button } from "../../components/Button/Button";
import { RouterValidationError, type RouterClient } from "../../router/client";
import type { Task } from "../../router/types";
import styles from "./BulkImportPanel.module.css";

export interface BulkImportPanelProps {
  sessionId: string;
  routerClient: RouterClient;
  onClose: () => void;
  onImported?: (tasks: Task[]) => void;
}

interface ParseResult {
  tasks?: Partial<Task>[];
  error?: string;
}

function parse(sessionId: string, text: string): ParseResult {
  let json: unknown;
  try {
    json = JSON.parse(text);
  } catch {
    return { error: "Not valid JSON." };
  }
  if (!Array.isArray(json)) {
    return { error: "Expected a JSON array of tasks." };
  }
  return { tasks: json.map((t) => ({ session_id: sessionId, ...t })) };
}

export function BulkImportPanel({
  sessionId,
  routerClient,
  onClose,
  onImported,
}: BulkImportPanelProps) {
  const [text, setText] = useState("");
  const [parseError, setParseError] = useState<string | null>(null);
  const [parsedCount, setParsedCount] = useState<number | null>(null);
  const [fieldErrors, setFieldErrors] = useState<{ field: string; message: string }[]>([]);
  const [importing, setImporting] = useState(false);

  function handleValidate() {
    const result = parse(sessionId, text);
    setFieldErrors([]);
    if (result.error) {
      setParseError(result.error);
      setParsedCount(null);
    } else {
      setParseError(null);
      setParsedCount(result.tasks!.length);
    }
  }

  async function handleImport() {
    const result = parse(sessionId, text);
    if (result.error) {
      setParseError(result.error);
      return;
    }
    setParseError(null);
    setFieldErrors([]);
    setImporting(true);
    try {
      const tasks = await routerClient.importTasks(result.tasks!);
      onImported?.(tasks);
      onClose();
    } catch (e) {
      if (e instanceof RouterValidationError) {
        setFieldErrors(e.errors);
      } else {
        setParseError(e instanceof Error ? e.message : "Import failed");
      }
    } finally {
      setImporting(false);
    }
  }

  return (
    <SideSheet width={480}>
      <SideSheetHeader>Bulk import tasks</SideSheetHeader>
      <SideSheetBody>
        <textarea
          className={styles.textarea}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder='[{"title": "..."}]'
          aria-label="Tasks JSON"
        />
        {parseError && <p className={styles.error}>{parseError}</p>}
        {parsedCount !== null && !parseError && (
          <p className={styles.info}>{parsedCount} task(s) parsed.</p>
        )}
        {fieldErrors.length > 0 && (
          <ul className={styles.fieldErrors}>
            {fieldErrors.map((fe, i) => (
              <li key={i}>
                {fe.field}: {fe.message}
              </li>
            ))}
          </ul>
        )}
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="secondary" onClick={handleValidate}>
          Validate
        </Button>
        <Button onClick={handleImport} disabled={importing}>
          {importing ? "Importing…" : "Import"}
        </Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
