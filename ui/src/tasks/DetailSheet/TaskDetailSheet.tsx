import { useEffect, useState } from "react";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { Button } from "../../components/Button/Button";
import { Markdown } from "../../conversation/ChatRenderer/markdown";
import type { RouterClient } from "../../router/client";
import type { Note, NoteType, Task, TaskStatus } from "../../router/types";
import { DependencyPicker } from "./DependencyPicker";
import { NoteGroup } from "./NoteGroup";
import styles from "./TaskDetailSheet.module.css";

export interface TaskDetailSheetProps {
  task: Task;
  tasksById: Record<string, Task>;
  sessionTasks: Task[];
  routerClient: RouterClient;
  onClose: () => void;
  onSaved?: (task: Task) => void;
}

const STATUS_OPTIONS: TaskStatus[] = [
  "ready",
  "in_progress",
  "blocked",
  "needs_review",
  "done",
  "wont_do",
];

const NOTE_GROUPS: { type: NoteType; label: string }[] = [
  { type: "blocker", label: "Blocker" },
  { type: "decision", label: "Decision" },
  { type: "context", label: "Context" },
  { type: "risk", label: "Risk" },
  { type: "follow_up", label: "Follow-up" },
  { type: "observation", label: "Observation" },
];

/**
 * Local form state is always a full Task draft object — every save is a
 * full-replace PUT (§5), never a per-field PATCH, so every field edit here
 * mutates this one object rather than firing its own request.
 */
export function TaskDetailSheet({
  task,
  tasksById,
  sessionTasks,
  routerClient,
  onClose,
  onSaved,
}: TaskDetailSheetProps) {
  const [draft, setDraft] = useState<Task>(task);
  const [notes, setNotes] = useState<Note[]>(task.notes ?? []);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setDraft(task);
    setNotes(task.notes ?? []);
  }, [task]);

  function set<K extends keyof Task>(key: K, value: Task[K]) {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    try {
      const saved = await routerClient.updateTask(draft);
      onSaved?.(saved);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to save task");
    } finally {
      setSaving(false);
    }
  }

  async function handleConfirmNote(noteId: number) {
    await routerClient.confirmNote(task.id, noteId);
    setNotes((prev) =>
      prev.map((n) => (n.id === noteId ? { ...n, status: "confirmed" } : n)),
    );
  }

  async function handleDiscardNote(noteId: number) {
    await routerClient.discardNote(task.id, noteId);
    setNotes((prev) => prev.filter((n) => n.id !== noteId));
  }

  return (
    <SideSheet width={480} className={styles.sheet}>
      <SideSheetHeader>
        <input
          className={styles.titleInput}
          value={draft.title}
          onChange={(e) => set("title", e.target.value)}
          aria-label="Title"
        />
      </SideSheetHeader>
      <SideSheetBody>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Status</span>
          <select
            value={draft.status}
            onChange={(e) => set("status", e.target.value as TaskStatus)}
          >
            {STATUS_OPTIONS.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>Labels</span>
          <input
            value={(draft.labels ?? []).join(", ")}
            onChange={(e) =>
              set(
                "labels",
                e.target.value
                  .split(",")
                  .map((l) => l.trim())
                  .filter(Boolean),
              )
            }
          />
        </label>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>Description</span>
          <textarea
            value={draft.description ?? ""}
            onChange={(e) => set("description", e.target.value)}
          />
          {draft.description && <Markdown text={draft.description} className={styles.preview} />}
        </label>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>Acceptance criteria</span>
          <textarea
            value={draft.acceptance_criteria ?? ""}
            onChange={(e) => set("acceptance_criteria", e.target.value)}
          />
          {draft.acceptance_criteria && (
            <Markdown text={draft.acceptance_criteria} className={styles.preview} />
          )}
        </label>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>Scope</span>
          <textarea value={draft.scope ?? ""} onChange={(e) => set("scope", e.target.value)} />
        </label>

        <div className={styles.field}>
          <span className={styles.fieldLabel}>Dependencies</span>
          <DependencyPicker
            task={draft}
            tasksById={tasksById}
            sessionTasks={sessionTasks}
            dependencies={draft.dependencies ?? []}
            onChange={(dependencies) => set("dependencies", dependencies)}
          />
        </div>

        <div className={styles.notes}>
          {NOTE_GROUPS.map(({ type, label }) => (
            <NoteGroup
              key={type}
              type={type}
              label={label}
              notes={notes.filter((n) => n.type === type)}
              onConfirm={handleConfirmNote}
              onDiscard={handleDiscardNote}
            />
          ))}
        </div>

        {error && <p className={styles.error}>{error}</p>}
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
        <Button onClick={handleSave} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
