import { useRef, useState } from "react";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Select, type SelectOption } from "../../../components/Select/Select";
import { effectiveFastModelMode } from "../../../router/fastModel";
import type { Config, FastModelMode, FastModelStatus } from "../../../router/types";
import {
  SUPPORT_LABELS,
  SUPPORT_TONES,
  behaviorLabel,
  capabilityTags,
  differingBehaviors,
} from "../providerBehaviors";
import { optionsWith } from "../settingHelpers";
import { SettingRow, SettingTag } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { InlineLink, StatusLine, type StatusTone } from "../StatusLine";
import { useCredentials } from "../state/useCredentials";
import { useFastModelStatus } from "../state/useFastModelStatus";
import { useProviderMatrix, type UseProviderMatrix } from "../state/useProviderMatrix";
import type { UseConfig } from "../state/useConfig";
import input from "../settingInput.module.css";
import styles from "./ModelsSection.module.css";

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

// What goes quiet without a Fast Model (spec §11/§13).
const SKIPPED = "titles, summaries, digests, retros, commit messages";

/**
 * The Fast Model's status line. "provider" mode depends on the live
 * GET /fast-model/status signal, since Config alone can't say whether the
 * resolved default provider implements Completer. Dot: ok when it answers,
 * warn while dormant (no endpoint yet) or still checking, bad when the
 * provider can't do completions at all, and muted when it's switched off —
 * a choice, not a fault.
 */
export function fastModelStatusLine(
  mode: FastModelMode,
  draft: Config,
  status: FastModelStatus | null,
  statusLoading: boolean,
): { tone: StatusTone; text: string } {
  if (mode === "off") {
    return { tone: "muted", text: `Fast Model is off. These are skipped: ${SKIPPED}.` };
  }
  if (mode === "endpoint") {
    return draft.fast_model_base_url
      ? { tone: "ok", text: "Fast Model is active." }
      : { tone: "warn", text: `No endpoint yet. Until one is set, these are skipped: ${SKIPPED}.` };
  }
  if (statusLoading || !status) return { tone: "warn", text: "Checking Fast Model status…" };
  if (status.provider_completer) {
    return { tone: "ok", text: `Fast Model is active, running via ${status.provider_name}.` };
  }
  const who = status.provider_name ?? "Your provider";
  return {
    tone: "bad",
    text: `${who} can't do background completions. Use an API endpoint or switch provider. Until then, these are skipped: ${SKIPPED}.`,
  };
}

export interface ModelsSectionProps {
  config: UseConfig;
}

/** Default provider and what it can't do, the Fast Model, and the provider capability matrix. */
export function ModelsSection({ config }: ModelsSectionProps) {
  const { draft, fieldErrors, setDraft } = config;
  const matrix = useProviderMatrix();
  const credentials = useCredentials();
  const fastModelStatus = useFastModelStatus();
  const [capabilitiesOpen, setCapabilitiesOpen] = useState(false);
  const capabilitiesRef = useRef<HTMLDetailsElement>(null);

  if (!draft) return null;

  const providerName = draft.default_provider ?? "";
  const provider = matrix.providers.find((p) => p.name === providerName);
  const tags = provider ? capabilityTags(provider, matrix.behaviors) : [];
  const fastModelMode = effectiveFastModelMode(draft);
  const status = fastModelStatusLine(fastModelMode, draft, fastModelStatus.status, fastModelStatus.loading);

  const showAllCapabilities = () => {
    const details = capabilitiesRef.current;
    if (!details) return;
    details.open = true;
    setCapabilitiesOpen(true);
    details.scrollIntoView({ block: "start" });
  };

  return (
    <div className={styles.panel}>
      <SettingsCard title="Sessions">
        <SettingRow
          label="Default provider"
          htmlFor="default-provider"
          help="Model, persona, trust and isolation are set from the Launchpad's Save as my defaults."
        >
          <div className={styles.provider}>
            <Select
              id="default-provider"
              value={providerName}
              options={[
                { value: "", label: "— none —" },
                ...optionsWith(
                  matrix.providers.map((p) => ({ value: p.name, label: p.name })),
                  providerName,
                ).filter((o) => o.value),
              ]}
              onChange={(name) => setDraft({ ...draft, default_provider: name })}
            />
            {provider && (
              <div className={styles.caps} data-testid="provider-capability-tags">
                {tags.map((tag) => (
                  <SettingTag key={tag.behavior} tone={tag.tone}>
                    {tag.label}
                  </SettingTag>
                ))}
                <InlineLink onClick={showAllCapabilities}>All capabilities →</InlineLink>
              </div>
            )}
          </div>
        </SettingRow>
      </SettingsCard>

      <h3 className={styles.groupTitle}>Fast Model</h3>
      <SettingsCard>
        <SettingRow label="Runs on" labelId="fast-model-mode-label">
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
                className={`${input.input} ${input.mono}`}
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
                className={`${input.input} ${input.mono}`}
                placeholder="gpt-4o-mini"
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

        <StatusLine tone={status.tone} testId="fast-model-status">
          {status.text}
        </StatusLine>
      </SettingsCard>

      <SettingsCard>
        <details
          ref={capabilitiesRef}
          id="provider-capabilities"
          className={styles.disclosure}
          open={capabilitiesOpen}
          onToggle={(e) => setCapabilitiesOpen(e.currentTarget.open)}
        >
          <summary className={styles.summary}>
            <span className={styles.summaryLabel}>Provider capabilities</span>
            {!matrix.loading && (
              <SettingTag>
                {matrix.providers.length} provider{matrix.providers.length === 1 ? "" : "s"} ·{" "}
                {matrix.behaviors.length} behaviour{matrix.behaviors.length === 1 ? "" : "s"}
              </SettingTag>
            )}
          </summary>
          <CapabilityMatrix matrix={matrix} />
        </details>
      </SettingsCard>
    </div>
  );
}

type MatrixFilter = "differences" | "all";

const MATRIX_FILTERS: SegmentedOption<MatrixFilter>[] = [
  { value: "differences", label: "Differences only" },
  { value: "all", label: "All" },
];

/** Behaviours down, providers across (§11); "Differences only" hides rows every provider does natively. */
function CapabilityMatrix({ matrix }: { matrix: UseProviderMatrix }) {
  const { providers, behaviors, loading } = matrix;
  const [filter, setFilter] = useState<MatrixFilter>("differences");

  if (loading) return <div className={styles.loading}>Loading providers…</div>;

  const rows = filter === "all" ? behaviors : differingBehaviors(providers, behaviors);

  return (
    <>
      <div className={styles.filters}>
        <span id="capabilities-filter-label">Show:</span>
        <SegmentedControl
          aria-labelledby="capabilities-filter-label"
          value={filter}
          options={MATRIX_FILTERS}
          onChange={setFilter}
        />
      </div>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">Behaviour</th>
            {providers.map((provider) => (
              <th key={provider.name} scope="col">
                {provider.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((behavior) => (
            <tr key={behavior.behavior}>
              <th scope="row">
                {behaviorLabel(behavior.behavior)}
                <span className={styles.level}>{behavior.level}</span>
              </th>
              {providers.map((provider) => {
                const support = provider.matrix[behavior.behavior];
                return (
                  <td key={provider.name}>
                    {support && <SettingTag tone={SUPPORT_TONES[support]}>{SUPPORT_LABELS[support]}</SettingTag>}
                  </td>
                );
              })}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={providers.length + 1} className={styles.empty}>
                Every provider does every behaviour natively.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </>
  );
}
