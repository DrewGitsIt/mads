import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Popover } from "../../components/Popover/Popover";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import type { Task } from "../../router/types";
import {
  blockedReason,
  graphStatusAccessibleLabel,
  graphStatusOf,
  type GraphStatus,
} from "../GraphView/graphStatus";
import { StatusGlyph } from "../GraphView/StatusGlyph";
import styles from "./DependencyPicker.module.css";

export interface DependencyPickerProps {
  task: Task;
  tasksById: Record<string, Task>;
  sessionTasks: Task[];
  dependencies: string[];
  onChange: (dependencies: string[]) => void;
  /** Click-through navigation to a dependency/unblocked task's own detail pane. */
  onSelectTask?: (task: Task) => void;
  /** Live run state, keyed by task id — feeds the in-progress subline. Absent when no pipelineClient is wired. */
  taskRunState?: Record<string, TaskRunState>;
  reducedMotion?: boolean;
  /** Show the dependencies without the add/remove controls. */
  readOnly?: boolean;
}

/**
 * Cheap, non-authoritative "would create a cycle" heads-up: a DFS from the
 * candidate forward through its own dependents, checking whether it reaches
 * back to `task`. No network call — the real check is GET /tasks/cycles
 * after save (§5). Don't conflate this with GraphView's authoritative
 * server-side cycle highlighting.
 */
function wouldCreateCycle(
  task: Task,
  candidateId: string,
  dependencies: string[],
  tasksById: Record<string, Task>,
): boolean {
  if (candidateId === task.id) return true;
  const visited = new Set<string>();
  const stack = [candidateId];
  while (stack.length > 0) {
    const current = stack.pop()!;
    if (current === task.id) return true;
    if (visited.has(current)) continue;
    visited.add(current);
    stack.push(...(tasksById[current]?.dependencies ?? []));
  }
  return false;
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** Local, small, and intentionally not extracted — see HANDOFF.md's judgment-call note. */
function relativeLanded(iso: string): string {
  const d = new Date(iso);
  const days = Math.floor((startOfDay(new Date()) - startOfDay(d)) / 86_400_000);
  if (days <= 0) return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
  if (days === 1) return "yesterday";
  if (days < 7) return `${days}d ago`;
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

type SummaryBucket = "done" | "in_progress" | "ready" | "blocked";

const SUMMARY_ORDER: { key: SummaryBucket; label: string; ribbonClass: string }[] = [
  { key: "done", label: "done", ribbonClass: styles.rDone },
  { key: "in_progress", label: "running", ribbonClass: styles.rProg },
  { key: "ready", label: "ready", ribbonClass: styles.rReady },
  { key: "blocked", label: "blocked", ribbonClass: styles.rBlocked },
];

/** Draft folds into ready for the 4-bucket summary/ribbon — see HANDOFF.md's rationale. */
function summaryBucketOf(status: GraphStatus): SummaryBucket {
  return status === "draft" ? "ready" : status;
}

function depSubline(
  dep: Task,
  tasksById: Record<string, Task>,
  taskRunState: Record<string, TaskRunState> | undefined,
): string {
  const status = graphStatusOf(dep);
  if (status === "done") return `done · landed ${relativeLanded(dep.updated_at)}`;
  if (status === "in_progress") {
    const run = taskRunState?.[dep.id];
    if (run?.currentStage) {
      const stageAttempts = run.attempts.filter((a) => a.stage === run.currentStage);
      const attempt = stageAttempts.length > 0 ? Math.max(...stageAttempts.map((a) => a.attempt)) : 1;
      return `in progress · ${run.currentStage} · attempt ${attempt}`;
    }
    return "in progress";
  }
  if (status === "blocked") {
    return `blocked · ${blockedReason(dep, tasksById, taskRunState) ?? graphStatusAccessibleLabel(dep, tasksById)}`;
  }
  return "ready";
}

export function DependencyPicker({
  task,
  tasksById,
  sessionTasks,
  dependencies,
  onChange,
  onSelectTask,
  taskRunState,
  reducedMotion,
  readOnly = false,
}: DependencyPickerProps) {
  const [pickerOpen, setPickerOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const depTasks = dependencies.map((id) => tasksById[id]).filter((t): t is Task => !!t);

  const counts: Record<SummaryBucket, number> = { done: 0, in_progress: 0, ready: 0, blocked: 0 };
  for (const dep of depTasks) {
    counts[summaryBucketOf(graphStatusOf(dep))]++;
  }
  const total = depTasks.length;
  const activeBuckets = SUMMARY_ORDER.filter((b) => counts[b.key] > 0);

  const candidates = useMemo(
    () =>
      sessionTasks.filter(
        (t) => t.id !== task.id && t.title.toLowerCase().includes(query.trim().toLowerCase()),
      ),
    [sessionTasks, task.id, query],
  );

  const unblocks = sessionTasks.filter((t) => (t.dependencies ?? []).includes(task.id));

  useEffect(() => {
    setHighlightedIndex((i) => Math.min(i, Math.max(candidates.length - 1, 0)));
  }, [candidates.length]);

  function openPicker() {
    setPickerOpen(true);
    setQuery("");
    setHighlightedIndex(0);
    requestAnimationFrame(() => inputRef.current?.focus());
  }

  function closePicker() {
    setPickerOpen(false);
  }

  function candidateBadge(candidate: Task): string | undefined {
    if (dependencies.includes(candidate.id)) return "already a dependency";
    if (wouldCreateCycle(task, candidate.id, dependencies, tasksById)) return "would create a cycle";
    return undefined;
  }

  function addDependency(candidate: Task) {
    if (candidateBadge(candidate)) return;
    onChange([...dependencies, candidate.id]);
    inputRef.current?.focus();
  }

  function handleInputKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlightedIndex((i) => Math.min(i + 1, candidates.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlightedIndex((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const candidate = candidates[highlightedIndex];
      if (candidate) addDependency(candidate);
    }
  }

  return (
    <div className={styles.picker}>
      <div className={styles.sec}>
        <span>Depends on</span>
        {total > 0 && (
          <span className={styles.sum}>
            {total} · {activeBuckets.map((b) => `${counts[b.key]} ${b.label}`).join(" · ")}
          </span>
        )}
      </div>

      {total > 0 && (
        <div className={styles.ribbon}>
          {activeBuckets.map((b) => (
            <i key={b.key} className={b.ribbonClass} style={{ flex: counts[b.key] }} />
          ))}
        </div>
      )}

      {total === 0 ? (
        <div className={styles.empty}>
          <b>No dependencies</b>
          Runs as soon as a pipeline slot frees. Add one to sequence it behind another task.
        </div>
      ) : (
        <ul className={styles.deps}>
          {depTasks.map((dep) => {
            const status = graphStatusOf(dep);
            return (
              <li key={dep.id} className={styles.dep}>
                <button
                  type="button"
                  className={styles.depMain}
                  onClick={() => onSelectTask?.(dep)}
                >
                  <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
                  <span className={styles.depText}>
                    <span className={styles.depTitle}>{dep.title}</span>
                    <span className={styles.depSub}>{depSubline(dep, tasksById, taskRunState)}</span>
                  </span>
                  <span className={styles.go} aria-hidden="true">
                    ›
                  </span>
                </button>
                {!readOnly && (
                  <button
                    type="button"
                    className={styles.remove}
                    title="Remove dependency"
                    aria-label={`Remove dependency: ${dep.title}`}
                    onClick={() => onChange(dependencies.filter((id) => id !== dep.id))}
                  >
                    ×
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {!readOnly && (
        <Popover
          open={pickerOpen}
          onClose={closePicker}
          anchor={
            !pickerOpen && (
              <button type="button" className={styles.addbtn} onClick={openPicker}>
                + Add dependency…
              </button>
            )
          }
        >
          <div className={styles.pickerBox}>
            <input
              ref={inputRef}
              className={styles.pickerInput}
              placeholder="Search tasks…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={handleInputKeyDown}
            />
            <div className={styles.cands}>
              {candidates.map((candidate, index) => {
                const badge = candidateBadge(candidate);
                const status = graphStatusOf(candidate);
                return (
                  <div
                    key={candidate.id}
                    className={[
                      styles.cand,
                      badge && styles.candDisabled,
                      index === highlightedIndex && styles.candHighlighted,
                    ]
                      .filter(Boolean)
                      .join(" ")}
                    onMouseEnter={() => setHighlightedIndex(index)}
                    onClick={() => addDependency(candidate)}
                  >
                    <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
                    <span className={styles.candTitle}>{candidate.title}</span>
                    {badge && (
                      <span className={badge === "would create a cycle" ? styles.warn : styles.already}>{badge}</span>
                    )}
                  </div>
                );
              })}
            </div>
            <div className={styles.pickhint}>↑↓ to move · Enter to add · Esc to close</div>
          </div>
        </Popover>
      )}

      <div className={styles.secUnblocks}>
        <span>Unblocks</span>
        {unblocks.length > 0 && (
          <span className={styles.sum}>
            {unblocks.length} {unblocks.length === 1 ? "task waits" : "tasks wait"} on this one
          </span>
        )}
      </div>
      {unblocks.length === 0 ? (
        <div className={styles.empty}>Nothing waits on this task.</div>
      ) : (
        <ul className={styles.unblocksList}>
          {unblocks.map((t) => {
            const status = graphStatusOf(t);
            return (
              <li key={t.id}>
                <button type="button" className={styles.ub} onClick={() => onSelectTask?.(t)}>
                  <StatusGlyph status={status} animate={status === "in_progress" && !reducedMotion} />
                  <span>{t.title}</span>
                  <span className={styles.arr} aria-hidden="true">
                    →
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
