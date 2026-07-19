import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import { Combobox } from "../../components/Combobox/Combobox";
import { Select } from "../../components/Select/Select";
import type { TemplateStage } from "../../router/types";
import type { FieldError } from "./useTemplateValidation";
import styles from "./StageCard.module.css";

export interface StageCardProps {
  stage: TemplateStage;
  index: number;
  earlierStageNames: string[];
  errors: FieldError[]; // pre-filtered to this stage, field already stripped of "stages[N]." prefix
  knownModelIds: string[];
  inheritedModel?: string;
  onChange: (stage: TemplateStage) => void;
  onRemove: () => void;
}

const THEN_OPTIONS = [
  { value: "", label: "—" },
  { value: "block", label: "Block" },
];

function errorFor(errors: FieldError[], field: string): string | undefined {
  return errors.find((e) => e.field === field)?.message;
}

/** One pipeline stage's fields, covering pkg/template's actual grammar (§6). */
export function StageCard({
  stage,
  index,
  earlierStageNames,
  errors,
  knownModelIds,
  inheritedModel,
  onChange,
  onRemove,
}: StageCardProps) {
  const mode = stage.command !== undefined ? "command" : "agent";
  const stageError = errorFor(errors, "");

  const loopBackOptions = [
    { value: "", label: "—" },
    ...earlierStageNames.map((name) => ({ value: name, label: name })),
  ];

  function setMode(next: "agent" | "command") {
    if (next === "agent") {
      onChange({ ...stage, agent: stage.agent ?? "auto", command: undefined });
    } else {
      onChange({ ...stage, command: stage.command ?? "", agent: undefined });
    }
  }

  function setOnFail(patch: Partial<NonNullable<TemplateStage["on_fail"]>>) {
    onChange({ ...stage, on_fail: { ...stage.on_fail, ...patch } });
  }

  return (
    <Card className={styles.card}>
      <div className={styles.header}>
        <span className={styles.index}>Stage {index + 1}</span>
        <Button variant="ghost" size="sm" onClick={onRemove}>
          Remove
        </Button>
      </div>

      <label className={styles.field}>
        <span className={styles.fieldLabel}>Name</span>
        <input value={stage.name} onChange={(e) => onChange({ ...stage, name: e.target.value })} />
      </label>
      {errorFor(errors, "name") && <div className={styles.error}>{errorFor(errors, "name")}</div>}

      <div className={styles.modeToggle} role="group" aria-label="Stage type">
        <Chip tone="neutral" pressed={mode === "agent"} onClick={() => setMode("agent")}>
          Agent
        </Chip>
        <Chip tone="neutral" pressed={mode === "command"} onClick={() => setMode("command")}>
          Command
        </Chip>
      </div>
      {stageError && <div className={styles.error}>{stageError}</div>}

      {mode === "agent" ? (
        <>
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Agent</span>
            <input value={stage.agent ?? ""} onChange={(e) => onChange({ ...stage, agent: e.target.value })} />
          </label>
          {errorFor(errors, "agent") && <div className={styles.error}>{errorFor(errors, "agent")}</div>}

          <label className={styles.field}>
            <span className={styles.fieldLabel}>Model</span>
            <Combobox
              value={stage.model ?? ""}
              options={knownModelIds}
              placeholder={inheritedModel ? `${inheritedModel} (inherited)` : "Inherit"}
              onChange={(next) => {
                const trimmed = next.trim();
                onChange({ ...stage, model: trimmed || undefined });
              }}
            />
          </label>
          {errorFor(errors, "model") && <div className={styles.error}>{errorFor(errors, "model")}</div>}
        </>
      ) : (
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Command</span>
          <input value={stage.command ?? ""} onChange={(e) => onChange({ ...stage, command: e.target.value })} />
        </label>
      )}

      <label className={styles.field}>
        <span className={styles.fieldLabel}>When</span>
        <input value={stage.when ?? ""} onChange={(e) => onChange({ ...stage, when: e.target.value })} />
      </label>
      {errorFor(errors, "when") && <div className={styles.error}>{errorFor(errors, "when")}</div>}

      <details className={styles.onFail} open={!!stage.on_fail}>
        <summary className={styles.onFailSummary}>On fail</summary>
        <div className={styles.onFailBody}>
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Loop back to</span>
            <Select
              value={stage.on_fail?.loop_back ?? ""}
              options={loopBackOptions}
              onChange={(next) => setOnFail({ loop_back: next || undefined })}
            />
          </label>
          {errorFor(errors, "on_fail.loop_back") && (
            <div className={styles.error}>{errorFor(errors, "on_fail.loop_back")}</div>
          )}
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Max loops</span>
            <input
              type="number"
              min={1}
              value={stage.on_fail?.max_loops ?? ""}
              onChange={(e) => setOnFail({ max_loops: Number(e.target.value) || undefined })}
            />
          </label>
          {errorFor(errors, "on_fail.max_loops") && (
            <div className={styles.error}>{errorFor(errors, "on_fail.max_loops")}</div>
          )}
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Then</span>
            <Select
              value={stage.on_fail?.then ?? ""}
              options={THEN_OPTIONS}
              onChange={(next) => setOnFail({ then: next || undefined })}
            />
          </label>
          {errorFor(errors, "on_fail.then") && (
            <div className={styles.error}>{errorFor(errors, "on_fail.then")}</div>
          )}
        </div>
      </details>
    </Card>
  );
}
