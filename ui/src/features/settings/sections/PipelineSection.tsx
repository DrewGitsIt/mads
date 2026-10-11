import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { NumberField } from "../../../components/NumberField/NumberField";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Select, type SelectOption } from "../../../components/Select/Select";
import { Switch } from "../../../components/Switch/Switch";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { useCredentials } from "../state/useCredentials";
import { useFastModelStatus } from "../state/useFastModelStatus";
import { effectiveFastModelMode } from "../../../router/fastModel";
import type { UseConfig } from "../state/useConfig";
import type {
  AgentSelectionMode,
  Config,
  FastModelMode,
  FastModelStatus,
  ModelTier,
  SynthesisMode,
} from "../../../router/types";
import styles from "./PipelineSection.module.css";

// Option labels are display-only; the stored values are the lowercase enums.
const SYNTHESIS_MODES: SegmentedOption<SynthesisMode>[] = [
  { value: "off", label: "Off" },
  { value: "light", label: "Summary" },
  { value: "full", label: "Summary + DPA" },
];
const SYNTHESIS_COST_HINTS: Record<SynthesisMode, string> = {
  off: "No synthesis cost.",
  light: "~$0.01 per session — summary only.",
  full: "~$0.05 per session — summary + DPA generation.",
};

const AGENT_SELECTION_MODES: SegmentedOption<AgentSelectionMode>[] = [
  { value: "light", label: "Light" },
  { value: "full", label: "Full" },
];
const MODEL_TIERS: SegmentedOption<ModelTier>[] = [
  { value: "cheapest", label: "Cheapest" },
  { value: "balanced", label: "Balanced" },
  { value: "best", label: "Best" },
];
const MODEL_TIER_COST_HINTS: Record<ModelTier, string> = {
  cheapest: "Lowest cost, fastest turnaround.",
  balanced: "Balanced cost and quality — default.",
  best: "Highest cost, best for complex synthesis.",
};

const FAST_MODEL_MODES: SegmentedOption<FastModelMode>[] = [
  { value: "provider", label: "My provider" },
  { value: "endpoint", label: "API endpoint" },
  { value: "off", label: "Off" },
];

/** Stored credential names, plus the configured one even if it's since been deleted. */
function credentialOptions(names: string[], current: string): SelectOption[] {
  const known = current && !names.includes(current) ? [current, ...names] : names;
  return [{ value: "", label: "None" }, ...known.map((n) => ({ value: n, label: n }))];
}

// SLEEP_CAVEATS is the per-OS helper line under the prevent-sleep switch,
// keyed by the Router host's runtime.GOOS (Config.platform). Other OSes get
// no caveat.
const SLEEP_CAVEATS: Record<string, string> = {
  darwin: "Closing the lid on battery still sleeps your Mac. The run pauses and recovers when it wakes.",
  linux: "Closing a laptop lid may still suspend, depending on your desktop's lid settings.",
};

// fastModelStatusText is the Settings status line copy for each mode (spec
// §11/§13): "provider" mode depends on the live GET /fast-model/status
// signal since Config alone can't say whether the resolved default provider
// implements Completer.
function fastModelStatusText(
  mode: FastModelMode,
  draft: Config,
  status: FastModelStatus | null,
  statusLoading: boolean,
): string {
  if (mode === "off") {
    return "Fast Model is off — auto-summaries, auto-titles, linked-session digests, retros, and NL agent generation will skip silently.";
  }
  if (mode === "endpoint") {
    return draft.fast_model_base_url
      ? "Fast Model is active."
      : "Fast Model is dormant — auto-summaries, auto-titles, linked-session digests, retros, and NL agent generation will skip silently until an endpoint is configured.";
  }
  if (statusLoading || !status) return "Checking Fast Model status…";
  if (status.provider_completer) {
    return `Fast Model is active — running via ${status.provider_name}.`;
  }
  return "Your provider doesn't support background completions — configure an endpoint or switch provider.";
}

export interface PipelineSectionProps {
  config: UseConfig;
}

/** Synthesis mode + static cost hints, agent selection mode, model tier, cost alert threshold, fast model (HANDOFF §3). */
export function PipelineSection({ config }: PipelineSectionProps) {
  const { draft, fieldErrors, setDraft, save } = config;
  const [saving, setSaving] = useState(false);
  const credentials = useCredentials();
  const fastModelStatus = useFastModelStatus();

  if (!draft) return null;

  const fastModelMode = effectiveFastModelMode(draft);

  const handleSave = async () => {
    setSaving(true);
    await save();
    setSaving(false);
  };

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow
          label="Synthesis mode"
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

        <SettingRow
          label="Auto-approve synthesis"
          htmlFor="synthesis-auto-approve"
          help="Apply synthesized proposals immediately instead of leaving them pending for review."
        >
          <Switch
            id="synthesis-auto-approve"
            checked={draft.synthesis_auto_approve}
            onChange={(on) => setDraft({ ...draft, synthesis_auto_approve: on })}
          />
        </SettingRow>

        <SettingRow
          label="Agent selection mode"
          labelId="agent-selection-mode-label"
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
          label="Default model tier"
          labelId="default-model-tier-label"
          help={MODEL_TIER_COST_HINTS[draft.default_model_tier]}
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

        {/* Stored in cents; shown in dollars. */}
        <SettingRow label="Cost alert threshold" htmlFor="cost-alert-cents" error={fieldErrors.cost_alert_cents}>
          <NumberField
            id="cost-alert-cents"
            prefix="$"
            min={0}
            step={0.25}
            value={(draft.cost_alert_cents ?? 0) / 100}
            onChange={(dollars) => setDraft({ ...draft, cost_alert_cents: Math.round(dollars * 100) })}
          />
        </SettingRow>

        <SettingRow
          label="Stage timeout"
          htmlFor="pipeline-stage-timeout-minutes"
          help="How long a single pipeline stage (one agent invocation) may run before it's killed as stuck. Applies to both UI-launched and CLI runs."
          error={fieldErrors.pipeline_stage_timeout_minutes}
        >
          <NumberField
            id="pipeline-stage-timeout-minutes"
            min={0}
            unit="min"
            value={draft.pipeline_stage_timeout_minutes}
            onChange={(n) => setDraft({ ...draft, pipeline_stage_timeout_minutes: n })}
          />
        </SettingRow>

        <SettingRow
          label="Prevent system sleep while a pipeline is running"
          htmlFor="prevent-sleep"
          help={
            <>
              <div>Changes apply to runs started afterward.</div>
              {draft.platform && SLEEP_CAVEATS[draft.platform] && <div>{SLEEP_CAVEATS[draft.platform]}</div>}
            </>
          }
        >
          <Switch
            id="prevent-sleep"
            checked={!draft.allow_sleep_during_pipelines}
            onChange={(on) => setDraft({ ...draft, allow_sleep_during_pipelines: !on })}
          />
        </SettingRow>
      </SettingsCard>

      <SettingsCard title="Fast Model">
        <SettingRow label="Backend" labelId="fast-model-mode-label">
          <SegmentedControl
            id="fast-model-mode"
            aria-labelledby="fast-model-mode-label"
            value={fastModelMode}
            options={FAST_MODEL_MODES}
            onChange={(fast_model_mode) => setDraft({ ...draft, fast_model_mode })}
          />
        </SettingRow>

        {fastModelMode === "endpoint" && (
          <>
            <SettingRow nested label="Endpoint" htmlFor="fast-model-base-url" error={fieldErrors.fast_model_base_url}>
              <input
                id="fast-model-base-url"
                type="text"
                spellCheck={false}
                className={styles.input}
                placeholder="https://api.example.com/v1"
                value={draft.fast_model_base_url ?? ""}
                onChange={(e) => setDraft({ ...draft, fast_model_base_url: e.target.value })}
              />
            </SettingRow>

            <SettingRow nested label="Model" htmlFor="fast-model">
              <input
                id="fast-model"
                type="text"
                spellCheck={false}
                className={styles.input}
                value={draft.fast_model ?? ""}
                onChange={(e) => setDraft({ ...draft, fast_model: e.target.value })}
              />
            </SettingRow>

            <SettingRow nested label="Credential" htmlFor="fast-model-api-key-env">
              <Select
                id="fast-model-api-key-env"
                value={draft.fast_model_api_key_env ?? ""}
                options={credentialOptions(credentials.names, draft.fast_model_api_key_env ?? "")}
                onChange={(name) => setDraft({ ...draft, fast_model_api_key_env: name })}
              />
            </SettingRow>
          </>
        )}

        <div className={styles.status}>
          {fastModelStatusText(fastModelMode, draft, fastModelStatus.status, fastModelStatus.loading)}
        </div>
      </SettingsCard>

      <div className={styles.actions}>
        <Button variant="primary" onClick={() => void handleSave()} disabled={saving}>
          Save
        </Button>
      </div>
    </div>
  );
}
