import { NumberField } from "../../../components/NumberField/NumberField";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Switch } from "../../../components/Switch/Switch";
import type { AgentSelectionMode, ModelTier, SynthesisMode } from "../../../router/types";
import { restartTag } from "../settingHelpers";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import type { UseConfig } from "../state/useConfig";
import styles from "./SynthesisSection.module.css";

// Option labels are display-only; the stored values are the lowercase enums.
const SYNTHESIS_MODES: SegmentedOption<SynthesisMode>[] = [
  { value: "off", label: "Off" },
  { value: "light", label: "Summary" },
  { value: "full", label: "Summary + DPA" },
];
const SYNTHESIS_COST_HINTS: Record<SynthesisMode, string> = {
  off: "No synthesis cost.",
  light: "About $0.01 per session: summary only.",
  full: "About $0.05 per session: summary plus DPA generation.",
};

const MODEL_TIERS: SegmentedOption<ModelTier>[] = [
  { value: "cheapest", label: "Cheapest" },
  { value: "balanced", label: "Balanced" },
  { value: "best", label: "Best" },
];
const MODEL_TIER_HINTS: Record<ModelTier, string> = {
  cheapest: "Lowest cost, fastest turnaround.",
  balanced: "Balanced cost and quality.",
  best: "Highest cost, best for complex synthesis.",
};

const AGENT_SELECTION_MODES: SegmentedOption<AgentSelectionMode>[] = [
  { value: "light", label: "Light" },
  { value: "full", label: "Full" },
];

export interface SynthesisSectionProps {
  config: UseConfig;
}

/**
 * Synthesis mode with its static cost hint; the rest only matters while
 * synthesis is on, so it hangs off it and hides when it's off — unless a
 * field there has an error to show.
 */
export function SynthesisSection({ config }: SynthesisSectionProps) {
  const { draft, fieldErrors, setDraft } = config;
  if (!draft) return null;

  const dependentError =
    fieldErrors.synthesis_auto_approve ||
    fieldErrors.default_model_tier ||
    fieldErrors.agent_selection_mode ||
    fieldErrors.synthesis_debounce_ms;
  const showDependents = draft.synthesis_mode !== "off" || Boolean(dependentError);

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow
          label="Synthesis"
          labelId="synthesis-mode-label"
          help={SYNTHESIS_COST_HINTS[draft.synthesis_mode]}
          error={fieldErrors.synthesis_mode}
        >
          <SegmentedControl
            id="synthesis-mode"
            aria-labelledby="synthesis-mode-label"
            value={draft.synthesis_mode}
            options={SYNTHESIS_MODES}
            onChange={(synthesis_mode) => setDraft({ ...draft, synthesis_mode })}
          />
        </SettingRow>

        {showDependents && (
          <>
            <SettingRow
              nested
              label="Apply proposals automatically"
              htmlFor="synthesis-auto-approve"
              help="Otherwise they wait for your review."
              error={fieldErrors.synthesis_auto_approve}
            >
              <Switch
                id="synthesis-auto-approve"
                checked={draft.synthesis_auto_approve}
                onChange={(on) => setDraft({ ...draft, synthesis_auto_approve: on })}
              />
            </SettingRow>

            <SettingRow
              nested
              label="Model tier"
              labelId="default-model-tier-label"
              help={MODEL_TIER_HINTS[draft.default_model_tier]}
              error={fieldErrors.default_model_tier}
            >
              <SegmentedControl
                id="default-model-tier"
                aria-labelledby="default-model-tier-label"
                value={draft.default_model_tier}
                options={MODEL_TIERS}
                onChange={(default_model_tier) => setDraft({ ...draft, default_model_tier })}
              />
            </SettingRow>

            <SettingRow
              nested
              label="Agent selection"
              labelId="agent-selection-mode-label"
              help="How hard mads looks for the best-matched agent for follow-up work."
              error={fieldErrors.agent_selection_mode}
            >
              <SegmentedControl
                id="agent-selection-mode"
                aria-labelledby="agent-selection-mode-label"
                value={draft.agent_selection_mode}
                options={AGENT_SELECTION_MODES}
                onChange={(agent_selection_mode) => setDraft({ ...draft, agent_selection_mode })}
              />
            </SettingRow>

            <SettingRow
              nested
              label="Wait before synthesizing"
              htmlFor="synthesis-debounce-ms"
              tag={restartTag("synthesis_debounce_ms")}
              help="Lets a burst of activity settle first."
              error={fieldErrors.synthesis_debounce_ms}
            >
              <NumberField
                id="synthesis-debounce-ms"
                min={0}
                step={250}
                unit="ms"
                value={draft.synthesis_debounce_ms}
                onChange={(n) => setDraft({ ...draft, synthesis_debounce_ms: n })}
              />
            </SettingRow>
          </>
        )}
      </SettingsCard>
    </div>
  );
}
