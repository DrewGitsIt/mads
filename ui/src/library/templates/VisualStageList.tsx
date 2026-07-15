import { Button } from "../../components/Button/Button";
import type { ParsedTemplate, TemplateStage } from "../../router/types";
import { StageCard } from "./StageCard";
import type { FieldError } from "./useTemplateValidation";
import styles from "./VisualStageList.module.css";

export interface VisualStageListProps {
  parsed: ParsedTemplate | null;
  parseError: string | null;
  errors: FieldError[];
  onChange: (parsed: ParsedTemplate) => void;
}

const STAGE_FIELD = /^stages\[(\d+)\](?:\.(.+))?$/;

function errorsForStage(errors: FieldError[], index: number): FieldError[] {
  return errors
    .map((e) => {
      const match = STAGE_FIELD.exec(e.field);
      if (!match || Number(match[1]) !== index) return null;
      return { field: match[2] ?? "", message: e.message };
    })
    .filter((e): e is FieldError => e !== null);
}

function topLevelErrors(errors: FieldError[]): FieldError[] {
  return errors.filter((e) => !STAGE_FIELD.test(e.field));
}

function newStage(): TemplateStage {
  return { name: "", agent: "auto" };
}

/** Pure render+edit projection of `source` (via the parsed prop) — see TemplateEditor. */
export function VisualStageList({ parsed, parseError, errors, onChange }: VisualStageListProps) {
  if (parseError || !parsed) {
    return (
      <div className={styles.parseError}>
        Can't render — fix YAML syntax first.
        {parseError && <div className={styles.parseErrorDetail}>{parseError}</div>}
      </div>
    );
  }

  function updateStage(index: number, stage: TemplateStage) {
    const stages = parsed!.stages.slice();
    stages[index] = stage;
    onChange({ ...parsed!, stages });
  }

  function removeStage(index: number) {
    onChange({ ...parsed!, stages: parsed!.stages.filter((_, i) => i !== index) });
  }

  function addStage() {
    onChange({ ...parsed!, stages: [...parsed!.stages, newStage()] });
  }

  const topErrors = topLevelErrors(errors);

  return (
    <div className={styles.list}>
      <label className={styles.nameField}>
        Template name
        <input value={parsed.name} onChange={(e) => onChange({ ...parsed, name: e.target.value })} />
      </label>
      {topErrors.map((e) => (
        <div key={e.field + e.message} className={styles.topError}>
          {e.field ? `${e.field}: ` : ""}
          {e.message}
        </div>
      ))}
      {parsed.stages.map((stage, index) => (
        <StageCard
          key={index}
          stage={stage}
          index={index}
          earlierStageNames={parsed.stages.slice(0, index).map((s) => s.name)}
          errors={errorsForStage(errors, index)}
          onChange={(next) => updateStage(index, next)}
          onRemove={() => removeStage(index)}
        />
      ))}
      <Button variant="secondary" onClick={addStage}>
        Add stage
      </Button>
    </div>
  );
}
