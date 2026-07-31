import { useEffect, useMemo, useRef, useState } from "react";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { Button } from "../../components/Button/Button";
import { CapabilityPickerField } from "../../components/CapabilityPickerField/CapabilityPickerField";
import { Chip } from "../../components/Chip/Chip";
import { Markdown } from "../../conversation/ChatRenderer/markdown";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { BlockedTaskFeedback } from "../../pipeline/Board/BlockedTaskFeedback";
import { StageTimeline } from "../../pipeline/DrillIn/StageTimeline";
import type { RouterClient } from "../../router/client";
import type { Capability, Note, NoteType, Task, TaskStatus, Workspace } from "../../router/types";
import { useGraphRunState } from "../GraphView/useGraphRunState";
import { usePrefersReducedMotion } from "../GraphView/usePrefersReducedMotion";
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
  /** Opt-in — no live run-state subscription (and no EventSource) is ever created without it. */
  pipelineClient?: PipelineClient;
  onSelectTask?: (task: Task) => void;
  /** Ambient capabilities the task's workspace already provides — rendered read-only here. */
  workspace?: Workspace | null;
  capabilities?: Capability[];
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

function autosize(el: HTMLTextAreaElement) {
  el.style.height = "auto";
  el.style.height = `${el.scrollHeight}px`;
}

/**
 * Resting state is the rendered Markdown (or a placeholder when empty);
 * clicking or pressing Enter on it swaps in an autofocused textarea with
 * the raw text. Commits to the draft on blur or Cmd/Ctrl+Enter — Escape
 * discards the in-flight edit without touching the draft.
 */
function EditableTextField({
  label,
  value,
  placeholder,
  onCommit,
}: {
  label: string;
  value: string;
  placeholder: string;
  onCommit: (value: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draftValue, setDraftValue] = useState(value);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const skipBlurRef = useRef(false);

  function startEditing() {
    skipBlurRef.current = false;
    setDraftValue(value);
    setEditing(true);
  }

  function commit() {
    setEditing(false);
    onCommit(draftValue);
  }

  function cancel() {
    skipBlurRef.current = true;
    setEditing(false);
  }

  useEffect(() => {
    if (!editing || !textareaRef.current) return;
    const el = textareaRef.current;
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    autosize(el);
  }, [editing]);

  if (editing) {
    return (
      <textarea
        ref={textareaRef}
        className={styles.editor}
        value={draftValue}
        aria-label={label}
        onChange={(e) => {
          setDraftValue(e.target.value);
          autosize(e.target);
        }}
        onBlur={() => {
          if (skipBlurRef.current) {
            skipBlurRef.current = false;
            return;
          }
          commit();
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            cancel();
          } else if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
            e.preventDefault();
            commit();
          }
        }}
      />
    );
  }

  return (
    <div
      className={styles.editableField}
      tabIndex={0}
      role="button"
      aria-label={`Edit ${label}`}
      onClick={startEditing}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          startEditing();
        }
      }}
    >
      {value ? (
        <Markdown text={value} className={styles.preview} />
      ) : (
        <span className={styles.placeholder}>{placeholder}</span>
      )}
      <span className={styles.editIcon} aria-hidden="true">
        ✎
      </span>
    </div>
  );
}

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
  pipelineClient,
  onSelectTask,
  workspace,
  capabilities = [],
}: TaskDetailSheetProps) {
  const [draft, setDraft] = useState<Task>(task);
  const [notes, setNotes] = useState<Note[]>(task.notes ?? []);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const runState = useGraphRunState(draft.session_id, pipelineClient);
  const reducedMotion = usePrefersReducedMotion();

  const inheritedCapabilities = useMemo(() => workspace?.capabilities ?? [], [workspace]);
  const taskCapabilityOptions = useMemo(
    () => capabilities.filter((c) => !inheritedCapabilities.includes(c.name)),
    [capabilities, inheritedCapabilities],
  );

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

        {pipelineClient && (() => {
          const taskFold = runState?.taskRunState[draft.id];
          const hasRunHistory = !!taskFold && (taskFold.attempts.length > 0 || !!taskFold.currentStage);
          if (!hasRunHistory && draft.status !== "blocked") return null;
          return (
            <div
              className={styles.runState}
              data-blocked={draft.status === "blocked" || undefined}
            >
              <span className={styles.fieldLabel}>Pipeline run</span>
              {hasRunHistory && (
                <>
                  {taskFold?.currentStage && (
                    <p className={styles.stage}>
                      {draft.status === "blocked"
                        ? `blocked at ${taskFold.currentStage}`
                        : taskFold.currentStage}
                    </p>
                  )}
                  <StageTimeline attempts={taskFold?.attempts ?? []} />
                </>
              )}
              {draft.status === "blocked" && (
                <BlockedTaskFeedback taskId={draft.id} notes={notes} client={pipelineClient} />
              )}
            </div>
          );
        })()}

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

        <div className={styles.field}>
          <span className={styles.fieldLabel}>Capabilities</span>
          {inheritedCapabilities.length > 0 && (
            <div className={styles.chips}>
              {inheritedCapabilities.map((name) => (
                <Chip key={name} tone="info">
                  {name}
                </Chip>
              ))}
            </div>
          )}
          <CapabilityPickerField
            label="Task-specific capabilities"
            capabilities={taskCapabilityOptions}
            value={draft.required_capabilities ?? []}
            onChange={(names) => set("required_capabilities", names)}
          />
        </div>

        <div className={styles.field}>
          <span className={styles.fieldLabel}>Description</span>
          <EditableTextField
            label="Description"
            value={draft.description ?? ""}
            placeholder="Add a description…"
            onCommit={(value) => set("description", value)}
          />
        </div>

        <div className={styles.field}>
          <span className={styles.fieldLabel}>Acceptance criteria</span>
          <EditableTextField
            label="Acceptance criteria"
            value={draft.acceptance_criteria ?? ""}
            placeholder="Add acceptance criteria…"
            onCommit={(value) => set("acceptance_criteria", value)}
          />
        </div>

        <div className={styles.field}>
          <span className={styles.fieldLabel}>Scope</span>
          <EditableTextField
            label="Scope"
            value={draft.scope ?? ""}
            placeholder="Add scope…"
            onCommit={(value) => set("scope", value)}
          />
        </div>

        <DependencyPicker
          task={draft}
          tasksById={tasksById}
          sessionTasks={sessionTasks}
          dependencies={draft.dependencies ?? []}
          onChange={(dependencies) => set("dependencies", dependencies)}
          onSelectTask={onSelectTask}
          taskRunState={runState?.taskRunState}
          reducedMotion={reducedMotion}
        />

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
