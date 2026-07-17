import { useEffect, useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import { router } from "../../../../router/client";
import type { SessionTrust } from "../../../../router/types";
import { TRUST_LEVELS } from "./trustLevels";
import pillStyles from "./pills.module.css";

export interface TrustPillProps {
  value?: SessionTrust;
  onChange: (trust: SessionTrust) => void;
}

// No workspace-level trust default exists yet (see Session.Trust) — this
// pill has no precedence to pre-fill from, unlike Workspace/Agent/Model.
export function TrustPill({ value, onChange }: TrustPillProps) {
  const [open, setOpen] = useState(false);
  const [defaultTrust, setDefaultTrust] = useState<SessionTrust>("limited");
  const selected = TRUST_LEVELS.find((t) => t.value === value);
  const defaultLevel = TRUST_LEVELS.find((t) => t.value === defaultTrust);

  useEffect(() => {
    router
      .getConfig()
      .then((config) => setDefaultTrust(config.default_trust))
      .catch(() => setDefaultTrust("limited"));
  }, []);

  const triggerLabel = selected?.label ?? `${defaultLevel?.label ?? "Limited"} (default)`;

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
        {TRUST_LEVELS.map((t) => (
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
