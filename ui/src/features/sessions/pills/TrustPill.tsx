import { useState } from "react";
import { Caret } from "../../../components/Caret/Caret";
import { Popover } from "../../../components/Popover/Popover";
import type {
  AgentPermissions,
  IntentMatrix,
  ProviderBehavior,
  ProviderSupport,
  ResolvedSessionField,
  SessionTrust,
} from "../../../router/types";
import { agentIntentNotes } from "../../../permissions/intents";
import { trustLevels } from "./trustLevels";
import { LockedPill, type PillLock } from "./LockedPill";
import pillStyles from "./pills.module.css";

export interface TrustPillProps {
  value?: SessionTrust;
  resolved?: ResolvedSessionField;
  /** The selected provider's capability matrix — see trustLevels.ts. */
  matrix?: Record<ProviderBehavior, ProviderSupport>;
  /** The selected provider's name, for Auto-review's "(runs as Limited on …)" note. */
  provider?: string;
  /** The selected agent's declared permissions, if any (Library agent record). */
  agentPermissions?: AgentPermissions;
  /** The selected provider's per-intent enforcement declaration — see providers/provider/intent.go. */
  intentMatrix?: IntentMatrix;
  onChange: (trust: SessionTrust) => void;
  /** Fixed by context (e.g. a link): renders the read-only LockedPill with the reason as its tooltip. */
  locked?: PillLock;
  /** Overrides the "staged override" accent; default: any value is staged. The /new card passes "differs from this session". */
  modified?: boolean;
}

export function TrustPill({
  value,
  resolved,
  matrix,
  provider,
  agentPermissions,
  intentMatrix,
  onChange,
  locked,
  modified,
}: TrustPillProps) {
  const [open, setOpen] = useState(false);
  const levels = trustLevels(matrix, provider);
  const selected = levels.find((t) => t.value === value);
  const resolvedLevel = levels.find((t) => t.value === resolved?.value);
  const intentNotes = agentIntentNotes(agentPermissions, intentMatrix);

  const triggerLabel =
    selected?.label ?? (resolvedLevel ? `${resolvedLevel.label} (default)` : "Select trust");

  if (locked) return <LockedPill name="trust" label={triggerLabel} lock={locked} />;

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={[pillStyles.trigger, (modified ?? value !== undefined) ? pillStyles.modified : ""]
            .filter(Boolean)
            .join(" ")}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>trust</span> {triggerLabel}
          <Caret />
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
        {intentNotes.length > 0 && (
          <div className={pillStyles.agentIntents}>
            <span className={pillStyles.groupLabel}>This agent also declares</span>
            {intentNotes.map((n) => (
              <div key={n.intent} className={pillStyles.intentNote}>
                <span className={pillStyles.optionLabel}>{n.label}</span>
                <span className={pillStyles.optionDescription}>{n.note}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Popover>
  );
}
