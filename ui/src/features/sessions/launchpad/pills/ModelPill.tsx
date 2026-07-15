import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { routerGet } from "../../../../router/client";
import type { Model, Provider } from "../../../../router/types";
import pillStyles from "./pills.module.css";

export interface ModelPillProps {
  provider?: string;
  model?: string;
  onChange: (provider: string, model: Model) => void;
}

export function ModelPill({ provider, model, onChange }: ModelPillProps) {
  const [open, setOpen] = useState(false);
  const [modelsByProvider, setModelsByProvider] = useState<Record<string, Model[]>>({});

  useEffect(() => {
    let cancelled = false;
    routerGet<Provider[]>("/providers")
      .then(async (providers) => {
        const entries = await Promise.all(
          providers.map(
            async (p) => [p, await routerGet<Model[]>(`/providers/${p}/models`)] as const,
          ),
        );
        if (!cancelled) setModelsByProvider(Object.fromEntries(entries));
      })
      .catch(() => {
        if (!cancelled) setModelsByProvider({});
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const label = model ? `${model} · ${provider}` : "Select model";

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
