import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import type { UseConfig } from "../state/useConfig";
import type { AgentSelectionMode, ModelTier, SynthesisMode } from "../../../router/types";
import styles from "./PipelineSection.module.css";

const SYNTHESIS_MODES: SynthesisMode[] = ["off", "light", "full"];
const SYNTHESIS_COST_HINTS: Record<SynthesisMode, string> = {
  off: "No synthesis cost.",
  light: "~$0.01 per session — summary only.",
  full: "~$0.05 per session — summary + DPA generation.",
};

const AGENT_SELECTION_MODES: AgentSelectionMode[] = ["light", "full"];
const MODEL_TIERS: ModelTier[] = ["cheapest", "balanced", "best"];
const MODEL_TIER_COST_HINTS: Record<ModelTier, string> = {
  cheapest: "Lowest cost, fastest turnaround.",
  balanced: "Balanced cost and quality — default.",
  best: "Highest cost, best for complex synthesis.",
};

export interface PipelineSectionProps {
  config: UseConfig;
}

/** Synthesis mode + static cost hints, agent selection mode, model tier, cost alert threshold, fast model (HANDOFF §3). */
export function PipelineSection({ config }: PipelineSectionProps) {
  const { draft, fieldErrors, setDraft, save } = config;
  const [saving, setSaving] = useState(false);

  if (!draft) return null;

  const handleSave = async () => {
    setSaving(true);
    await save();
    setSaving(false);
  };

  return (
    <div className={styles.panel}>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="synthesis-mode">
          Synthesis mode
        </label>
        <select
          id="synthesis-mode"
          className={styles.input}
          value={draft.synthesis_mode}
          onChange={(e) => setDraft({ ...draft, synthesis_mode: e.target.value as SynthesisMode })}
        >
          {SYNTHESIS_MODES.map((mode) => (
            <option key={mode} value={mode}>
              {mode}
            </option>
          ))}
        </select>
        <div className={styles.hint}>{SYNTHESIS_COST_HINTS[draft.synthesis_mode]}</div>
        {fieldErrors.synthesis_mode && <div className={styles.error}>{fieldErrors.synthesis_mode}</div>}
      </div>

      <div className={styles.row}>
        <label className={styles.label} htmlFor="agent-selection-mode">
          Agent selection mode
        </label>
        <select
          id="agent-selection-mode"
          className={styles.input}
          value={draft.agent_selection_mode}
          onChange={(e) =>
            setDraft({ ...draft, agent_selection_mode: e.target.value as AgentSelectionMode })
          }
        >
          {AGENT_SELECTION_MODES.map((mode) => (
            <option key={mode} value={mode}>
              {mode}
            </option>
          ))}
        </select>
        {fieldErrors.agent_selection_mode && (
          <div className={styles.error}>{fieldErrors.agent_selection_mode}</div>
        )}
      </div>

      <div className={styles.row}>
        <label className={styles.label} htmlFor="default-model-tier">
          Default model tier
        </label>
        <select
          id="default-model-tier"
          className={styles.input}
          value={draft.default_model_tier}
          onChange={(e) => setDraft({ ...draft, default_model_tier: e.target.value as ModelTier })}
        >
          {MODEL_TIERS.map((tier) => (
            <option key={tier} value={tier}>
              {tier}
            </option>
          ))}
        </select>
        <div className={styles.hint}>{MODEL_TIER_COST_HINTS[draft.default_model_tier]}</div>
        {fieldErrors.default_model_tier && (
          <div className={styles.error}>{fieldErrors.default_model_tier}</div>
        )}
      </div>

      <div className={styles.row}>
        <label className={styles.label} htmlFor="cost-alert-cents">
          Cost alert threshold (cents)
        </label>
        <input
          id="cost-alert-cents"
          type="number"
          className={styles.input}
          value={draft.cost_alert_cents ?? 0}
          onChange={(e) => setDraft({ ...draft, cost_alert_cents: Number(e.target.value) })}
        />
        {fieldErrors.cost_alert_cents && <div className={styles.error}>{fieldErrors.cost_alert_cents}</div>}
      </div>

      <div className={styles.row}>
        <label className={styles.label} htmlFor="fast-model">
          Fast model
        </label>
        <input
          id="fast-model"
          type="text"
          className={styles.input}
          value={draft.fast_model ?? ""}
          onChange={(e) => setDraft({ ...draft, fast_model: e.target.value })}
        />
      </div>

      <Button variant="primary" onClick={() => void handleSave()} disabled={saving}>
        Save
      </Button>
    </div>
  );
}
