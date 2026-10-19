import { useCallback, useEffect, useReducer, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { Button } from "../../components/Button/Button";
import { Caret } from "../../components/Caret/Caret";
import { Chip } from "../../components/Chip/Chip";
import { Popover } from "../../components/Popover/Popover";
import { SegmentedControl } from "../../components/SegmentedControl/SegmentedControl";
import { runStages } from "../../pipeline/plan/runStages";
import type { PipelineRun, Template } from "../../router/types";
import { formatElapsed, formatRunWhen, outcomeTone, runOrdinal, spanMs } from "./runBarFormat";
import styles from "./RunBar.module.css";

export type RunBarView = "list" | "graph";

interface RunBarCommon {
  view: RunBarView;
  onViewChange: (view: RunBarView) => void;
  /** This session's runs, newest first; may include the live run (outcome "running"). */
  runs: PipelineRun[];
  /** The run being shown; undefined in Plan. In Live it may be a run `runs` hasn't caught up with yet. */
  selectedRunSeq?: number;
  onSelectRun: (runSeq: number | "new") => void;
  /** Template label for a run's menu row, e.g. "default-implementation v3". Omitted rows show none. */
  templateLabelOf?: (run: PipelineRun) => string | undefined;
}

export interface RunBarPlanProps extends RunBarCommon {
  mode: "plan";
  templates: Template[];
  templateId: string;
  /** The workspace's bound template; its menu row gets a "workspace default" chip. */
  defaultTemplateId?: string;
  onTemplateChange: (templateId: string) => void;
  /** Tasks that run if started now. At 0 the Run button is disabled. */
  runnableCount: number;
  /** At most one line; truncates with the full text in a tooltip. */
  warning?: string;
  /** Rejecting shows the error inline. */
  onStart: () => Promise<void>;
  /** "Edit in Library" in the template menu's footer; hidden when omitted. */
  onOpenLibrary?: () => void;
}

export interface RunBarLiveProps extends RunBarCommon {
  mode: "live";
  templateLabel?: string;
  startedAt?: string;
  /** Rejecting shows the error inline. */
  onCancel: () => Promise<void>;
  /** When set, Cancel is disabled and this explains why (its tooltip). */
  cancelDisabledReason?: string;
}

export interface RunBarPastProps extends RunBarCommon {
  mode: "past";
  outcome: string;
  templateLabel?: string;
  startedAt?: string;
  endedAt?: string;
  onNewRun: () => void;
}

export type RunBarProps = RunBarPlanProps | RunBarLiveProps | RunBarPastProps;

const VIEW_OPTIONS = [
  { value: "list" as const, label: "List" },
  { value: "graph" as const, label: "Graph" },
];

type Pop = "run" | "template" | null;

/**
 * The Tasks tab's run bar (docs/TASKS-PIPELINE-DESIGN.md): view toggle, the
 * run menu (this session's run history, plus New run), then per mode —
 * Plan: template menu, one warning line, Run N tasks; Live: template,
 * ticking elapsed time, Cancel run; Past: template, duration, New run.
 * Controlled: the caller owns every value and does the work behind each
 * callback.
 */
export function RunBar(props: RunBarProps) {
  const { view, onViewChange, runs, selectedRunSeq, onSelectRun, templateLabelOf, mode } = props;
  const [pop, setPop] = useState<Pop>(null);
  const [pending, setPending] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const runTriggerRef = useRef<HTMLButtonElement>(null);
  const templateTriggerRef = useRef<HTMLButtonElement>(null);

  const ticking = mode === "live" || (pop === "run" && runs.some((r) => r.outcome === "running"));
  const now = useNow(ticking);

  // A mode switch is a different run: drop the last one's error and menus.
  useEffect(() => {
    setActionError(null);
    setPop(null);
  }, [mode]);

  const closePop = useCallback(() => setPop(null), []);

  async function act(fn: () => Promise<void>) {
    setActionError(null);
    setPending(true);
    try {
      await fn();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  }

  const shownRun = selectedRunSeq !== undefined ? runs.find((r) => r.run_seq === selectedRunSeq) : undefined;
  const shownOutcome = mode === "live" ? "running" : mode === "past" ? props.outcome : undefined;

  const runTrigger = (
    <button
      ref={runTriggerRef}
      type="button"
      className={styles.trigger}
      aria-haspopup="menu"
      aria-expanded={pop === "run"}
      onClick={() => setPop((p) => (p === "run" ? null : "run"))}
    >
      {mode === "plan" || selectedRunSeq === undefined ? (
        <b className={styles.strong}>New run</b>
      ) : (
        <>
          <b className={styles.strong}>Run {runOrdinal(runs, selectedRunSeq)}</b>
          <OutcomeChip outcome={shownOutcome ?? shownRun?.outcome} />
        </>
      )}
      <Caret className={styles.caret} />
    </button>
  );

  const runMenu = (
    <Popover open={pop === "run"} onClose={closePop} anchor={runTrigger} className={styles.runPop}>
      <Menu label="Runs" triggerRef={runTriggerRef}>
        <MenuItem
          checked={mode === "plan"}
          onSelect={() => {
            setPop(null);
            runTriggerRef.current?.focus();
            onSelectRun("new");
          }}
          name="+ New run"
          meta="choose a template, then Run"
        />
        {runs.length > 0 && <div className={styles.menuHeading}>This session's runs</div>}
        {runs.map((run) => {
          const tpl = templateLabelOf?.(run);
          return (
            <MenuItem
              key={run.run_seq}
              checked={mode !== "plan" && run.run_seq === selectedRunSeq}
              onSelect={() => {
                setPop(null);
                runTriggerRef.current?.focus();
                onSelectRun(run.run_seq);
              }}
              name={
                <>
                  Run {runOrdinal(runs, run.run_seq)}
                  <OutcomeChip outcome={run.outcome} />
                </>
              }
              meta={formatRunWhen(run, now)}
              description={tpl}
            />
          );
        })}
      </Menu>
    </Popover>
  );

  let middle: ReactNode;
  let action: ReactNode;

  if (mode === "plan") {
    const { templates, templateId, defaultTemplateId, onTemplateChange, runnableCount, warning, onStart, onOpenLibrary } =
      props;
    const current = templates.find((t) => t.id === templateId);
    const templateTrigger = (
      <button
        ref={templateTriggerRef}
        type="button"
        className={styles.trigger}
        aria-haspopup="menu"
        aria-expanded={pop === "template"}
        aria-label={`Template: ${current?.name ?? templateId}`}
        onClick={() => setPop((p) => (p === "template" ? null : "template"))}
      >
        <span className={styles.key}>Template</span>
        <b className={styles.strong}>{current?.name ?? templateId}</b>
        {current && <span className={styles.key}>v{current.version}</span>}
        <Caret className={styles.caret} />
      </button>
    );
    middle = (
      <>
        <Popover open={pop === "template"} onClose={closePop} anchor={templateTrigger} className={styles.templatePop}>
          <div className={styles.menuHeading}>Run this session's ready tasks through…</div>
          <Menu label="Templates" triggerRef={templateTriggerRef}>
            {templates.map((t) => (
              <MenuItem
                key={t.id}
                checked={t.id === templateId}
                onSelect={() => {
                  setPop(null);
                  templateTriggerRef.current?.focus();
                  onTemplateChange(t.id);
                }}
                name={
                  <>
                    {t.name}
                    {t.id === defaultTemplateId && <Chip tone="neutral">workspace default</Chip>}
                  </>
                }
                meta={`v${t.version}`}
                description={t.description}
                extra={<StageChain template={t} />}
              />
            ))}
          </Menu>
          <div className={styles.menuFooter}>
            <span>Templates live in Library. A run keeps a frozen copy.</span>
            {onOpenLibrary && (
              <button
                type="button"
                className={styles.link}
                onClick={() => {
                  setPop(null);
                  onOpenLibrary();
                }}
              >
                Edit in Library ↗
              </button>
            )}
          </div>
        </Popover>
        {warning && (
          <span className={styles.warning} title={warning}>
            <span aria-hidden="true">⚠ </span>
            {warning}
          </span>
        )}
      </>
    );
    action = (
      <Button
        variant="primary"
        size="sm"
        disabled={runnableCount === 0 || pending}
        title={runnableCount === 0 ? "No ready tasks to run" : undefined}
        onClick={() => void act(onStart)}
      >
        <span aria-hidden="true">▶ </span>
        Run {runnableCount} task{runnableCount === 1 ? "" : "s"}
      </Button>
    );
  } else {
    const { templateLabel, startedAt } = props;
    const endedAt = mode === "past" ? props.endedAt : undefined;
    // Past with no end on record (an interrupted run) shows no clock rather than one still counting.
    const elapsed = startedAt && (mode === "live" || endedAt) ? formatElapsed(spanMs(startedAt, endedAt, now)) : undefined;
    middle = (
      <span className={styles.meta}>
        {templateLabel && <span>{templateLabel}</span>}
        {elapsed && (
          <span className={styles.elapsed} aria-label={mode === "live" ? `Elapsed ${elapsed}` : `Took ${elapsed}`}>
            {elapsed}
          </span>
        )}
      </span>
    );
    if (mode === "live") {
      const { onCancel, cancelDisabledReason } = props;
      action = (
        <Button
          variant="secondary"
          size="sm"
          disabled={Boolean(cancelDisabledReason) || pending}
          title={cancelDisabledReason}
          onClick={() => void act(onCancel)}
        >
          Cancel run
        </Button>
      );
    } else {
      action = (
        <Button variant="primary" size="sm" onClick={props.onNewRun}>
          New run
        </Button>
      );
    }
  }

  return (
    <div className={styles.bar}>
      <div className={styles.row}>
        <SegmentedControl aria-label="View" value={view} options={VIEW_OPTIONS} onChange={onViewChange} />
        {runMenu}
        {middle}
        <span className={styles.spacer} />
        {action}
      </div>
      {actionError && (
        <div className={styles.error} role="alert">
          {actionError}
        </div>
      )}
    </div>
  );
}

/** `Date.now()` as of this render; while `ticking`, re-renders each second. */
function useNow(ticking: boolean): number {
  const [, tick] = useReducer((x: number) => x + 1, 0);
  useEffect(() => {
    if (!ticking) return;
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [ticking]);
  return Date.now();
}

function OutcomeChip({ outcome }: { outcome?: string }) {
  if (!outcome) return null;
  return (
    <Chip tone={outcomeTone(outcome)}>
      {outcome === "running" && <span className={styles.spin} aria-hidden="true" />}
      {outcome}
    </Chip>
  );
}

function StageChain({ template }: { template: Template }) {
  const stages = runStages(template);
  if (stages.length === 0) return null;
  return (
    <span className={styles.chain} aria-label={`Stages: ${stages.map((s) => s.name).join(", ")}`}>
      {stages.map((s, i) => (
        <span key={s.name} className={styles.chainStep}>
          {i > 0 && (
            <span className={styles.chainArrow} aria-hidden="true">
              ›
            </span>
          )}
          <span className={[styles.stage, s.kind === "command" && styles.command].filter(Boolean).join(" ")}>
            {s.name}
            {s.loopBack && (
              <span
                className={styles.loop}
                title={`on_fail: loop_back ${s.loopBack}${s.maxLoops ? ` ×${s.maxLoops}` : ""}`}
              >
                {" ↺"}
              </span>
            )}
          </span>
        </span>
      ))}
    </span>
  );
}

/**
 * A menu of radio items: focus lands on the checked item (or the first) when
 * it opens, arrows / Home / End move between items, Enter or Space picks one.
 * Escape and outside clicks belong to the enclosing kit Popover; Escape also
 * hands focus back to the trigger.
 */
function Menu({
  label,
  triggerRef,
  children,
}: {
  label: string;
  triggerRef: RefObject<HTMLButtonElement | null>;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const items = menuItems(ref.current);
    (items.find((el) => el.getAttribute("aria-checked") === "true") ?? items[0])?.focus();
  }, []);

  function handleKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      triggerRef.current?.focus();
      return;
    }
    const items = menuItems(ref.current);
    if (items.length === 0) return;
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    let next: number | null = null;
    if (e.key === "ArrowDown") next = (i + 1) % items.length;
    else if (e.key === "ArrowUp") next = (i - 1 + items.length) % items.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = items.length - 1;
    if (next === null) return;
    e.preventDefault();
    items[next].focus();
  }

  return (
    <div ref={ref} role="menu" aria-label={label} className={styles.menu} onKeyDown={handleKeyDown}>
      {children}
    </div>
  );
}

function menuItems(root: HTMLElement | null): HTMLButtonElement[] {
  return root ? Array.from(root.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')) : [];
}

function MenuItem({
  checked,
  onSelect,
  name,
  meta,
  description,
  extra,
}: {
  checked: boolean;
  onSelect: () => void;
  name: ReactNode;
  meta?: string;
  description?: string;
  extra?: ReactNode;
}) {
  return (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={checked}
      tabIndex={-1}
      className={styles.item}
      onClick={onSelect}
    >
      <span className={styles.itemName}>{name}</span>
      {meta && <span className={styles.itemMeta}>{meta}</span>}
      {description && <span className={styles.itemDesc}>{description}</span>}
      {extra && <span className={styles.itemExtra}>{extra}</span>}
    </button>
  );
}
