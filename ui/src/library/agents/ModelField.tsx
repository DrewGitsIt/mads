import { useProviderModels } from "../../router/useProviderModels";
import styles from "./ModelField.module.css";

export interface ModelFieldProps {
  value?: string;
  onChange: (model: string) => void;
}

/** Validated model dropdown — only lists models the provider actually reports (§5). */
export function ModelField({ value, onChange }: ModelFieldProps) {
  const modelsByProvider = useProviderModels();

  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor="model-select">
        Preferred model
      </label>
      <select id="model-select" value={value ?? ""} onChange={(e) => onChange(e.target.value)}>
        <option value="">No preference</option>
        {Object.entries(modelsByProvider).map(([provider, models]) => (
          <optgroup key={provider} label={provider}>
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </div>
  );
}
