import { useState, type FormEvent, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { EnvLookedFor } from "../EnvLookedFor";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { useCredentials } from "../state/useCredentials";
import type { UseConfig } from "../state/useConfig";
import type { EnvMode, Profile } from "../../../router/types";
import { isCredentialActive } from "../profileDiff";
import styles from "./CredentialsSection.module.css";

// Option labels are display-only; the stored values are the lowercase enums.
const ENV_MODES: SegmentedOption<EnvMode>[] = [
  { value: "inherit", label: "Inherit" },
  { value: "isolated", label: "Isolated" },
];

/** Plain-language summary of what env_mode + counts mean for a spawned agent. */
function envSummary(mode: EnvMode, credentialCount: number, passthrough: string[]): string {
  const plural = credentialCount === 1 ? "" : "s";
  if (mode === "isolated") {
    let summary = `Isolated: base environment + ${credentialCount} credential${plural}`;
    if (passthrough.length > 0) {
      summary += ` + ${passthrough.length} passthrough (${passthrough.join(", ")})`;
    }
    return summary;
  }
  return `Inherited environment + ${credentialCount} mads credential${plural}`;
}

export interface CredentialsSectionProps {
  config: UseConfig;
  profiles: Profile[];
}

function boundProfiles(name: string, profiles: Profile[]): Profile[] {
  return profiles.filter((p) => {
    const bindings = p.snapshot.credential_bindings as string[] | undefined;
    return isCredentialActive(name, bindings);
  });
}

/**
 * Masked credential list + add/delete form + env-import picker (HANDOFF §8,
 * the security acceptance criterion). The value input is cleared
 * synchronously in this component's own submit handler, before add()'s
 * network call is awaited — never in a .then(). Every stored credential is
 * always listed; ones not bound to the live draft's credential_bindings are
 * dimmed with a toggle — toggling edits draft.credential_bindings (drift),
 * not the vault.
 */
export function CredentialsSection({ config, profiles }: CredentialsSectionProps) {
  const credentials = useCredentials();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [selectedCandidates, setSelectedCandidates] = useState<string[]>([]);
  const [showEnvImport, setShowEnvImport] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [passthroughInput, setPassthroughInput] = useState("");

  const draft = config.draft;

  const handleAdd = (e: FormEvent) => {
    e.preventDefault();
    const pendingName = name;
    const pendingValue = value;
    setName("");
    setValue("");
    void credentials.add(pendingName, pendingValue);
  };

  const toggleCandidate = (candidate: string) => {
    setSelectedCandidates((prev) =>
      prev.includes(candidate) ? prev.filter((n) => n !== candidate) : [...prev, candidate],
    );
  };

  const handleImport = async () => {
    await credentials.importFromEnv(selectedCandidates);
    setSelectedCandidates([]);
  };

  const openEnvImport = async () => {
    setShowEnvImport(true);
    await credentials.loadEnvCandidates();
  };

  const toggleActive = (credentialName: string) => {
    if (!draft) return;
    const current = draft.credential_bindings;
    const wasActive = isCredentialActive(credentialName, current);
    const base = current && current.length > 0 ? current : credentials.names;
    const next = wasActive ? base.filter((n) => n !== credentialName) : [...base, credentialName];
    config.setDraft({ ...draft, credential_bindings: next });
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    await credentials.remove(pendingDelete);
    setPendingDelete(null);
  };

  const boundTo = pendingDelete ? boundProfiles(pendingDelete, profiles) : [];

  const envMode: EnvMode = draft?.env_mode ?? "inherit";
  const envPassthrough = draft?.env_passthrough ?? [];
  const activeCredentialCount = credentials.names.filter((n) =>
    isCredentialActive(n, draft?.credential_bindings),
  ).length;

  const chooseEnvMode = (mode: EnvMode) => {
    if (!draft) return;
    config.setDraft({ ...draft, env_mode: mode });
  };

  const addPassthrough = () => {
    if (!draft) return;
    const trimmed = passthroughInput.trim();
    if (!trimmed || envPassthrough.includes(trimmed)) return;
    config.setDraft({ ...draft, env_passthrough: [...envPassthrough, trimmed] });
    setPassthroughInput("");
  };

  const removePassthrough = (varName: string) => {
    if (!draft) return;
    config.setDraft({ ...draft, env_passthrough: envPassthrough.filter((n) => n !== varName) });
  };

  const handlePassthroughKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      addPassthrough();
    }
  };

  return (
    <div className={styles.panel}>
      <section>
        <h3 className={styles.heading}>Credentials</h3>
        <ul className={styles.list}>
          {credentials.names.map((n) => {
            const active = isCredentialActive(n, draft?.credential_bindings);
            return (
              <li key={n} className={active ? styles.row : `${styles.row} ${styles.inactive}`}>
                <span className={styles.name}>{n}</span>
                {!active && <Chip tone="neutral">Inactive</Chip>}
                <span className={styles.masked} aria-hidden="true">
                  ••••••••
                </span>
                <Button size="sm" variant="ghost" onClick={() => toggleActive(n)}>
                  {active ? "Deactivate" : "Use in this profile"}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setPendingDelete(n)}>
                  Delete
                </Button>
              </li>
            );
          })}
          {credentials.names.length === 0 && <li className={styles.empty}>No credentials stored.</li>}
        </ul>
      </section>

      <section>
        <h3 className={styles.heading}>Environment</h3>
        <SettingsCard>
          <SettingRow
            label="Environment mode"
            labelId="env-mode-label"
            help={envSummary(envMode, activeCredentialCount, envPassthrough)}
          >
            <SegmentedControl
              aria-labelledby="env-mode-label"
              value={envMode}
              options={ENV_MODES}
              onChange={chooseEnvMode}
            />
          </SettingRow>
          {envMode === "isolated" && (
            <SettingRow nested label="Passthrough env vars" htmlFor="env-passthrough-input">
              <div className={styles.passthrough}>
                <div className={styles.passthroughChips}>
                  {envPassthrough.map((n) => (
                    <Chip key={n} tone="neutral" className={styles.chip}>
                      {n}
                      <button
                        type="button"
                        className={styles.remove}
                        aria-label={`Remove ${n}`}
                        onClick={() => removePassthrough(n)}
                      >
                        ✕
                      </button>
                    </Chip>
                  ))}
                  {envPassthrough.length === 0 && <span className={styles.empty}>No passthrough vars.</span>}
                </div>
                <div className={styles.passthroughAdd}>
                  <input
                    id="env-passthrough-input"
                    className={styles.input}
                    value={passthroughInput}
                    onChange={(e) => setPassthroughInput(e.target.value)}
                    onKeyDown={handlePassthroughKeyDown}
                    placeholder="AWS_PROFILE"
                  />
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={addPassthrough}
                    disabled={!passthroughInput.trim()}
                    aria-label="Add passthrough variable"
                  >
                    Add
                  </Button>
                </div>
              </div>
            </SettingRow>
          )}
        </SettingsCard>
      </section>

      <form className={styles.addForm} onSubmit={handleAdd}>
        <label className={styles.label} htmlFor="cred-name">
          Name
        </label>
        <input
          id="cred-name"
          className={styles.input}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="OPENAI_API_KEY"
        />
        <label className={styles.label} htmlFor="cred-value">
          Value
        </label>
        <input
          id="cred-value"
          type="password"
          className={styles.input}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <Button type="submit" variant="primary" disabled={!name.trim() || !value}>
          Add
        </Button>
      </form>

      <section>
        <h3 className={styles.heading}>Import from environment</h3>
        {!showEnvImport ? (
          <Button variant="secondary" onClick={() => void openEnvImport()}>
            Scan environment
          </Button>
        ) : (
          <div className={styles.envImport}>
            <ul className={styles.list}>
              {credentials.envCandidates.map((n) => (
                <li key={n}>
                  <label>
                    <input
                      type="checkbox"
                      checked={selectedCandidates.includes(n)}
                      onChange={() => toggleCandidate(n)}
                    />
                    {n}
                  </label>
                </li>
              ))}
              {credentials.envCandidates.length === 0 && (
                <li className={styles.empty}>
                  None of the variables mads looks for are set in the environment it was started from, or
                  they&apos;re already stored.
                </li>
              )}
            </ul>
            <EnvLookedFor names={credentials.envLookedFor} />
            <Button
              variant="primary"
              onClick={() => void handleImport()}
              disabled={selectedCandidates.length === 0}
            >
              Import selected
            </Button>
          </div>
        )}
      </section>

      <section>
        <h3 className={styles.heading}>Vault sources</h3>
        <div className={styles.empty}>
          No vault configured. Real vault integrations (1Password, AWS Secrets Manager) are a future release.
        </div>
      </section>

      <ConfirmDialog
        open={pendingDelete !== null}
        title={`Delete ${pendingDelete ?? ""}?`}
        description={
          boundTo.length > 0
            ? `Bound to ${boundTo.map((p) => p.name).join(", ")}. Deleting removes it from the vault entirely.`
            : "Not bound to any profile. Deleting removes it from the vault entirely."
        }
        confirmLabel="Delete"
        onConfirm={() => void confirmDelete()}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  );
}
