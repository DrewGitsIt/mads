import { useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { useProviderModels } from "../../../../router/useProviderModels";
import { modelDisplayName } from "../../../../router/modelDisplay";
import type { Model, ResolvedSessionField } from "../../../../router/types";
import pillStyles from "./pills.module.css";

export interface ModelPillProps {
  provider?: string;
  model?: string;
  resolved?: ResolvedSessionField;
  resolvedProvider?: string;
  onChange: (provider: string, model: Model) => void;
}

export function ModelPill({
  provider,
  model,
  resolved,
  resolvedProvider,
  onChange,
}: ModelPillProps) {
  const [open, setOpen] = useState(false);
  const modelsByProvider = useProviderModels();

  const models = modelsByProvider[provider ?? ""] ?? [];

  let label: string;
  if (model) {
    label = `${modelDisplayName(model, models) ?? model} · ${provider}`;
  } else if (resolved?.value) {
    const resolvedModels = modelsByProvider[resolvedProvider ?? ""] ?? [];
    label = `${modelDisplayName(resolved.value, resolvedModels) ?? resolved.value} · ${resolvedProvider} (default)`;
  } else {
    label = "Select model";
  }

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={pillStyles.trigger}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>model</span> {label}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {Object.entries(modelsByProvider).map(([providerName, models]) => (
          <div key={providerName}>
            <div className={pillStyles.groupLabel}>{providerName}</div>
            {models.map((m) => (
              <button
                key={m.id}
                type="button"
                className={[
                  pillStyles.option,
                  m.id === model && providerName === provider ? pillStyles.optionSelected : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() => {
                  onChange(providerName, m);
                  setOpen(false);
                }}
              >
                {m.name}
              </button>
            ))}
          </div>
        ))}
      </div>
    </Popover>
  );
}
