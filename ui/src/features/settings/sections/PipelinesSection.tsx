import { NumberField } from "../../../components/NumberField/NumberField";
import { Switch } from "../../../components/Switch/Switch";
import { restartTag } from "../settingHelpers";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import type { UseConfig } from "../state/useConfig";
import styles from "./PipelinesSection.module.css";

// SLEEP_CAVEATS is the per-OS helper line under the keep-awake switch,
// keyed by the Router host's runtime.GOOS (Config.platform). Other OSes get
// no caveat.
const SLEEP_CAVEATS: Record<string, string> = {
  darwin: "Closing the lid on battery still sleeps your Mac; the run pauses and recovers when it wakes.",
  linux: "Closing a laptop lid may still suspend, depending on your desktop's lid settings.",
};

export interface PipelinesSectionProps {
  config: UseConfig;
}

/** How multi-stage runs behave: concurrency, stage timeout, cost alert, keeping the machine awake. */
export function PipelinesSection({ config }: PipelinesSectionProps) {
  const { draft, fieldErrors, setDraft } = config;
  if (!draft) return null;

  const sleepCaveat = draft.platform ? SLEEP_CAVEATS[draft.platform] : undefined;

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow
          label="Concurrency limit"
          htmlFor="concurrency-limit"
          tag={restartTag("concurrency_limit")}
          help="How many agents can run at once."
          error={fieldErrors.concurrency_limit}
        >
          <NumberField
            id="concurrency-limit"
            min={1}
            value={draft.concurrency_limit}
            onChange={(n) => setDraft({ ...draft, concurrency_limit: n })}
          />
        </SettingRow>

        <SettingRow
          label="Stage timeout"
          htmlFor="pipeline-stage-timeout-minutes"
          help="A stage that runs longer than this is stopped as stuck. Applies to UI and CLI runs."
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

        {/* Stored in cents; shown in dollars. */}
        <SettingRow
          label="Cost alert"
          htmlFor="cost-alert-cents"
          help="Warn when one run spends more than this. 0 turns it off."
          error={fieldErrors.cost_alert_cents}
        >
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
          label="Keep the computer awake during runs"
          htmlFor="prevent-sleep"
          help={sleepCaveat ? `${sleepCaveat} Applies to runs started afterward.` : "Applies to runs started afterward."}
        >
          <Switch
            id="prevent-sleep"
            checked={!draft.allow_sleep_during_pipelines}
            onChange={(on) => setDraft({ ...draft, allow_sleep_during_pipelines: !on })}
          />
        </SettingRow>
      </SettingsCard>
    </div>
  );
}
