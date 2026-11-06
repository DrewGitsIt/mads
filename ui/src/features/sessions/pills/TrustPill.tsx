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
import { providerLabel } from "../../settings/providerBehaviors";
import { fullAccessOnly, fullAccessReason, trustLevels } from "./trustLevels";
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
  // The row under the pointer or keyboard focus — what the footer explains.
  const [hovered, setHovered] = useState<SessionTrust | undefined>();
  const levels = trustLevels(matrix, provider ? providerLabel(provider) : undefined);
  // A provider that runs at full trust only has nothing else to offer, so
  // Full is what's shown (and what the create payload sends —
  // buildCreateSessionPayload's fullTrustOnly), whatever was staged or
  // configured; the disabled rows' warnings say why.
  const fullOnly = fullAccessOnly(matrix);
  const reason = fullOnly ? fullAccessReason(provider ? providerLabel(provider) : "This provider") : undefined;
  const shown = fullOnly ? "full" : value;
  const selected = levels.find((t) => t.value === shown);
  const resolvedLevel = levels.find((t) => t.value === resolved?.value);
  const current = selected ?? resolvedLevel;
  const detail = levels.find((t) => t.value === hovered) ?? current;
  const intentNotes = agentIntentNotes(agentPermissions, intentMatrix);

  const triggerLabel =
    selected?.label ?? (resolvedLevel ? `${resolvedLevel.label} (default)` : "Select trust");
  // Only once the provider has declared its matrix: before that every
  // restricted level "hasn't said", and the pill would flash a warning.
  const triggerWarning = matrix ? current?.warning : undefined;

  if (locked) return <LockedPill name="trust" label={triggerLabel} lock={locked} />;

  return (
    <Popover
      open={open}
      onClose={() => {
        setOpen(false);
        setHovered(undefined);
      }}
      anchor={
        <button
          type="button"
          className={[pillStyles.trigger, !fullOnly && (modified ?? value !== undefined) ? pillStyles.modified : ""]
            .filter(Boolean)
            .join(" ")}
          title={triggerWarning ?? reason}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>trust</span> {triggerLabel}
          {triggerWarning && <WarnIcon />}
          <Caret />
        </button>
      }
    >
      <div className={[pillStyles.menu, pillStyles.trustMenu].join(" ")} onMouseLeave={() => setHovered(undefined)}>
        {levels.map((t) => (
          <button
            key={t.value}
            type="button"
            // aria-disabled, not disabled: a disabled button gets no hover
            // or focus, and the footer has to explain why it's unavailable.
            aria-disabled={t.unavailable || undefined}
            className={[
              pillStyles.option,
              pillStyles.trustOption,
              t.value === shown ? pillStyles.optionSelected : "",
              t.unavailable ? pillStyles.optionUnavailable : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onMouseEnter={() => setHovered(t.value)}
            onFocus={() => setHovered(t.value)}
            onClick={() => {
              if (t.unavailable) return;
              onChange(t.value);
              setOpen(false);
              setHovered(undefined);
            }}
          >
            <span className={pillStyles.optionLabel}>{t.label}</span>
            {t.warning && <WarnIcon />}
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
        {detail && (
          <div className={pillStyles.trustDetail} data-testid="trust-detail" aria-live="polite">
            <span>
              <strong>{detail.label}</strong>: {detail.summary}
            </span>
            {detail.warning && (
              <span className={pillStyles.trustWarning}>
                <WarnIcon small />
                {detail.warning}
              </span>
            )}
          </div>
        )}
      </div>
    </Popover>
  );
}

/**
 * The ⚠ beside a level the selected provider doesn't natively support. The
 * footer's small one sits before the sentence that says it, so it's hidden
 * from assistive tech rather than read twice.
 */
function WarnIcon({ small }: { small?: boolean }) {
  return (
    <span
      className={[pillStyles.warnIcon, small ? pillStyles.warnIconSmall : ""].filter(Boolean).join(" ")}
      {...(small ? { "aria-hidden": true } : { role: "img", "aria-label": "Not supported by this provider" })}
    >
      ⚠
    </span>
  );
}
