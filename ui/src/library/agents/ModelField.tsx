import { Select, type SelectOption } from "../../components";
import { useProviderModels } from "../../router/useProviderModels";
import styles from "./ModelField.module.css";

export interface ModelFieldProps {
  value?: string;
  onChange: (model: string) => void;
}

/** Validated model dropdown — only lists models the provider actually reports (§5). */
export function ModelField({ value, onChange }: ModelFieldProps) {
  const modelsByProvider = useProviderModels();

  const options: SelectOption[] = Object.entries(modelsByProvider).flatMap(([provider, models]) =>
    models.map((m) => ({ value: m.id, label: m.name, groupLabel: provider })),
  );

  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor="model-select">
        Preferred model
      </label>
      <Select id="model-select" value={value ?? ""} onChange={onChange} options={options} placeholder="No preference" />
    </div>
  );
}
