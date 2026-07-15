import { useState } from "react";
import { Popover } from "../../../../components/Popover/Popover";
import type { SessionTrust } from "../../../../router/types";
import pillStyles from "./pills.module.css";

const TRUST_LEVELS: { value: SessionTrust; label: string }[] = [
  { value: "full", label: "Full access" },
  { value: "limited", label: "Limited" },
  { value: "read_only", label: "Read-only" },
];

export interface TrustPillProps {
  value?: SessionTrust;
  onChange: (trust: SessionTrust) => void;
}

// No workspace-level trust default exists yet (see Session.Trust) — this
// pill has no precedence to pre-fill from, unlike Workspace/Agent/Model.
export function TrustPill({ value, onChange }: TrustPillProps) {
  const [open, setOpen] = useState(false);
  const selected = TRUST_LEVELS.find((t) => t.value === value);

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
          <span className={pillStyles.key}>trust</span> {selected?.label ?? "Prompt each time"}
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
            {t.label}
          </button>
        ))}
      </div>
    </Popover>
  );
}
