import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
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
        Name
        <input value={stage.name} onChange={(e) => onChange({ ...stage, name: e.target.value })} />
      </label>
      {errorFor(errors, "name") && <div className={styles.error}>{errorFor(errors, "name")}</div>}

      <div className={styles.modeToggle}>
        <label>
          <input type="radio" checked={mode === "agent"} onChange={() => setMode("agent")} />
          Agent
        </label>
        <label>
          <input type="radio" checked={mode === "command"} onChange={() => setMode("command")} />
          Command
        </label>
      </div>
      {stageError && <div className={styles.error}>{stageError}</div>}

      {mode === "agent" ? (
        <>
          <label className={styles.field}>
            Agent
            <input value={stage.agent ?? ""} onChange={(e) => onChange({ ...stage, agent: e.target.value })} />
          </label>
          {errorFor(errors, "agent") && <div className={styles.error}>{errorFor(errors, "agent")}</div>}

          <label className={styles.field}>
            Model
            <input
              list={`model-options-${index}`}
              value={stage.model ?? ""}
              placeholder={inheritedModel ? `${inheritedModel} (inherited)` : "Inherit"}
              onChange={(e) => {
                const trimmed = e.target.value.trim();
                onChange({ ...stage, model: trimmed || undefined });
              }}
            />
            <datalist id={`model-options-${index}`}>
              {knownModelIds.map((id) => (
                <option key={id} value={id} />
              ))}
            </datalist>
          </label>
          {errorFor(errors, "model") && <div className={styles.error}>{errorFor(errors, "model")}</div>}
        </>
      ) : (
        <label className={styles.field}>
          Command
          <input value={stage.command ?? ""} onChange={(e) => onChange({ ...stage, command: e.target.value })} />
        </label>
      )}

      <label className={styles.field}>
        When
        <input value={stage.when ?? ""} onChange={(e) => onChange({ ...stage, when: e.target.value })} />
      </label>
      {errorFor(errors, "when") && <div className={styles.error}>{errorFor(errors, "when")}</div>}

      <details className={styles.onFail} open={!!stage.on_fail}>
        <summary>On fail</summary>
        <label className={styles.field}>
          Loop back to
          <select
            value={stage.on_fail?.loop_back ?? ""}
            onChange={(e) => setOnFail({ loop_back: e.target.value || undefined })}
          >
            <option value="">—</option>
            {earlierStageNames.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
        {errorFor(errors, "on_fail.loop_back") && (
          <div className={styles.error}>{errorFor(errors, "on_fail.loop_back")}</div>
        )}
        <label className={styles.field}>
          Max loops
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
          Then
          <select value={stage.on_fail?.then ?? ""} onChange={(e) => setOnFail({ then: e.target.value || undefined })}>
            <option value="">—</option>
            <option value="block">Block</option>
          </select>
        </label>
        {errorFor(errors, "on_fail.then") && (
          <div className={styles.error}>{errorFor(errors, "on_fail.then")}</div>
        )}
      </details>
    </Card>
  );
}
