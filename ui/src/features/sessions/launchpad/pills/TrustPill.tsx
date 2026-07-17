import { useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import type {
  ProviderBehavior,
  ProviderSupport,
  ResolvedSessionField,
  SessionTrust,
} from "../../../../router/types";
import { trustLevels } from "./trustLevels";
import pillStyles from "./pills.module.css";

export interface TrustPillProps {
  value?: SessionTrust;
  resolved?: ResolvedSessionField;
  /** The selected provider's capability matrix — see trustLevels.ts. */
  matrix?: Record<ProviderBehavior, ProviderSupport>;
  onChange: (trust: SessionTrust) => void;
}

export function TrustPill({ value, resolved, matrix, onChange }: TrustPillProps) {
  const [open, setOpen] = useState(false);
  const levels = trustLevels(matrix);
  const selected = levels.find((t) => t.value === value);
  const resolvedLevel = levels.find((t) => t.value === resolved?.value);

  const triggerLabel =
    selected?.label ?? (resolvedLevel ? `${resolvedLevel.label} (default)` : "Select trust");

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
          <span className={pillStyles.key}>trust</span> {triggerLabel}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {levels.map((t) => (
          <button
            key={t.value}
            type="button"
            className={[pillStyles.option, t.value === value ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => {
              onChange(t.value);
              setOpen(false);
            }}
          >
            <span className={pillStyles.optionLabel}>{t.label}</span>
            <span className={pillStyles.optionDescription}>{t.description}</span>
          </button>
        ))}
      </div>
    </Popover>
  );
}
