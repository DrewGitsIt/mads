import { useEffect, useMemo, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import type { Template } from "../../router/types";
import { parse, serialize } from "./templateSync";
import { useTemplateValidation } from "./useTemplateValidation";
import { VisualStageList } from "./VisualStageList";
import { YamlSourceView } from "./YamlSourceView";
import styles from "./TemplateEditor.module.css";

const DEFAULT_SOURCE = "name: \nstages: []\n";

export interface TemplateEditorProps {
  template: Template | null;
  onClose: () => void;
  onCreate: (template: Partial<Template>) => Promise<Template>;
  onUpdate: (template: Template) => Promise<Template>;
  onDelete: (id: string) => Promise<void>;
}

/**
 * `source: string` is the only state — visual mode is a pure render+edit
 * projection of it (templateSync.parse/serialize), YAML mode binds to it
 * directly. There is never a second "visual model" that can drift (§6).
 */
export function TemplateEditor({ template, onClose, onCreate, onUpdate, onDelete }: TemplateEditorProps) {
  const [source, setSource] = useState(template?.source ?? DEFAULT_SOURCE);
  const [description, setDescription] = useState(template?.description ?? "");
  const [mode, setMode] = useState<"visual" | "yaml">("visual");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const { errors, validating, valid, validate } = useTemplateValidation();

  const { parsed, error: parseError } = useMemo(() => parse(source), [source]);

  // Opening an already-invalid template (e.g. hand-edited YAML that drifted
  // from the grammar) used to show nothing until the user clicked Validate.
  // Run the same check once on mount so its errors are on screen immediately.
  useEffect(() => {
    void validate(source);
    // Intentionally mount-only — re-running on every keystroke belongs to
    // the Validate button and Save, not this one-shot opening check.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function applyParsedChange(next: NonNullable<ReturnType<typeof parse>["parsed"]>) {
    setSource(serialize(next));
  }

  async function handleValidate() {
    await validate(source);
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    const ok = await validate(source);
    if (!ok) {
      setSaving(false);
      return;
    }
    const payload: Partial<Template> = { name: parsed?.name ?? "", description: description || undefined, source };
    try {
      if (template) {
        await onUpdate({ ...template, ...payload } as Template);
      } else {
        await onCreate(payload);
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    if (!template) return;
    setSaving(true);
    try {
      await onDelete(template.id);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving(false);
    }
  }

  return (
    <SideSheet width={640}>
      <SideSheetHeader>
        {template ? (
          <>
            {`Edit ${template.name}`}
            <Chip tone="neutral" className={styles.versionChip}>
              v{template.version}
            </Chip>
          </>
        ) : (
          "New template"
        )}
      </SideSheetHeader>
      <SideSheetBody>
        <div className={styles.form}>
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Description</span>
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>

          <div className={styles.modeToggle}>
            <Chip tone="neutral" pressed={mode === "visual"} onClick={() => setMode("visual")}>
              Visual
            </Chip>
            <Chip tone="neutral" pressed={mode === "yaml"} onClick={() => setMode("yaml")}>
              YAML
            </Chip>
          </div>

          {mode === "visual" ? (
            <VisualStageList
              parsed={parsed}
              parseError={parseError}
              errors={errors}
              onChange={applyParsedChange}
            />
          ) : (
            <YamlSourceView source={source} onChange={setSource} />
          )}

          <div className={styles.validateRow}>
            <Button variant="secondary" disabled={validating} onClick={() => void handleValidate()}>
              {validating ? "Validating…" : "Validate"}
            </Button>
            {valid === true && <span className={styles.valid}>Valid</span>}
          </div>

          {errors.length > 0 && (
            <ul className={styles.errorList}>
              {errors.map((e) => (
                <li key={e.field + e.message}>
                  {e.field ? `${e.field}: ` : ""}
                  {e.message}
                </li>
              ))}
            </ul>
          )}

          <p className={styles.runNote}>
            Editing this won't affect runs already in progress — in-flight runs freeze their own
            copy of the template at launch.
          </p>

          {error && <div className={styles.error}>{error}</div>}
        </div>
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="primary" disabled={saving} onClick={() => void handleSave()}>
          Save
        </Button>
        {template && !template.system && (
          <Button variant="ghost" disabled={saving} onClick={() => void handleDelete()}>
            Delete
          </Button>
        )}
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
