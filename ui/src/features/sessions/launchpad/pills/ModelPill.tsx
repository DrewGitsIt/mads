import { useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { useProviderCatalogs } from "../../../../router/useProviderModels";
import { modelDisplayName } from "../../../../router/modelDisplay";
import type { Model, ModelCatalog, ResolvedSessionField } from "../../../../router/types";
import pillStyles from "./pills.module.css";

// emptyNote explains an empty group. "Couldn't ask the harness" and "the
// harness has nothing" are different problems for the user — the first is
// usually a missing binary or a logged-out CLI and is worth surfacing
// verbatim, the second is just a fact.
function emptyNote(catalog: ModelCatalog): string {
  switch (catalog.state) {
    case "refreshing":
    case "pending":
      return "discovering models…";
    case "unavailable":
      return catalog.detail || "model discovery unavailable";
    default:
      return "no models";
  }
}

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
  const catalogs = useProviderCatalogs();

  const models = catalogs[provider ?? ""]?.models ?? [];

  let label: string;
  if (model) {
    label = `${modelDisplayName(model, models) ?? model} · ${provider}`;
  } else if (resolved?.value) {
    const resolvedModels = catalogs[resolvedProvider ?? ""]?.models ?? [];
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
        {Object.entries(catalogs).map(([providerName, catalog]) => (
          <div key={providerName}>
            <div className={pillStyles.groupLabel}>{providerName}</div>
            {catalog.models.length === 0 && (
              // An empty list means three different things and the old
              // menu collapsed all of them into a blank group.
              <div className={pillStyles.groupNote}>{emptyNote(catalog)}</div>
            )}
            {catalog.models.map((m) => (
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
