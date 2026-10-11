import { useState, type FormEvent, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import { SegmentedControl, type SegmentedOption } from "../../../components/SegmentedControl/SegmentedControl";
import { Switch } from "../../../components/Switch/Switch";
import { EnvLookedFor } from "../EnvLookedFor";
import { SettingRow, SettingTag } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { SettingsSheet } from "../SettingsSheet";
import { useCredentials } from "../state/useCredentials";
import type { UseConfig } from "../state/useConfig";
import type { EnvMode, Profile } from "../../../router/types";
import { isCredentialActive } from "../profileDiff";
import input from "../settingInput.module.css";
import styles from "./CredentialsSection.module.css";

// Option labels are display-only; the stored values are the lowercase enums.
const ENV_MODES: SegmentedOption<EnvMode>[] = [
  { value: "inherit", label: "Inherit" },
  { value: "isolated", label: "Isolated" },
];

/** Plain-language summary of what env_mode + counts mean for a spawned agent. */
function envSummary(mode: EnvMode, credentialCount: number, passthrough: string[]): string {
  const credentials = `${credentialCount} credential${credentialCount === 1 ? "" : "s"}`;
  if (mode === "isolated") {
    const vars = `${passthrough.length} passthrough variable${passthrough.length === 1 ? "" : "s"}`;
    return `Agents get a clean base environment, ${credentials} and ${vars}.`;
  }
  return `Agents get your shell's environment plus ${credentials}.`;
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
 * Masked credential list + inline add row + env-import picker in a side
 * sheet (HANDOFF §8, the security acceptance criterion). The value input is
 * cleared synchronously in this component's own submit handler, before
 * add()'s network call is awaited — never in a .then(). Every stored
 * credential is always listed; ones not bound to the live draft's
 * credential_bindings are dimmed, and each row's switch edits
 * draft.credential_bindings (drift, the save bar), not the vault.
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

  // Done once something landed: the sheet closes onto the list that now holds it.
  const handleImport = async () => {
    const imported = await credentials.importFromEnv(selectedCandidates);
    setSelectedCandidates([]);
    if (imported.length > 0) setShowEnvImport(false);
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
      <div>
        <SettingsCard title="Stored credentials">
          <ul className={styles.list}>
            {credentials.names.map((n) => {
              const active = isCredentialActive(n, draft?.credential_bindings);
              return (
                <li key={n} className={active ? styles.row : `${styles.row} ${styles.inactive}`}>
                  <span className={styles.name}>{n}</span>
                  {!active && <SettingTag>Not in this profile</SettingTag>}
                  <span className={styles.masked} aria-hidden="true">
                    ••••••••
                  </span>
                  <Switch checked={active} aria-label={`Use ${n} in this profile`} onChange={() => toggleActive(n)} />
                  <Button size="sm" variant="ghost" className={styles.danger} onClick={() => setPendingDelete(n)}>
                    Delete
                  </Button>
                </li>
              );
            })}
            {credentials.names.length === 0 && <li className={styles.empty}>No credentials stored.</li>}
          </ul>
          <form className={styles.addRow} onSubmit={handleAdd} aria-label="Add a credential">
            <input
              aria-label="Name"
              className={`${input.input} ${input.mono}`}
              value={name}
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => setName(e.target.value)}
              placeholder="NAME"
            />
            <input
              aria-label="Value"
              type="password"
              className={input.input}
              value={value}
              autoComplete="off"
              onChange={(e) => setValue(e.target.value)}
              placeholder="Value"
            />
            <Button type="submit" size="sm" disabled={!name.trim() || !value}>
              Add
            </Button>
          </form>
        </SettingsCard>
        <div className={styles.note}>
          <span>The switch sets whether this profile passes the credential to agents.</span>
          <Button size="sm" onClick={() => void openEnvImport()}>
            Import from environment…
          </Button>
        </div>
      </div>

      <SettingsCard title="Agent environment">
        <SettingRow
          label="Environment"
          labelId="env-mode-label"
          help={envSummary(envMode, activeCredentialCount, envPassthrough)}
        >
          <SegmentedControl
            id="env-mode"
            aria-labelledby="env-mode-label"
            value={envMode}
            options={ENV_MODES}
            onChange={chooseEnvMode}
          />
        </SettingRow>
        {envMode === "isolated" && (
          <SettingRow
            nested
            label="Also pass through"
            htmlFor="env-passthrough-input"
            help="Variables from your shell that isolated agents still get."
          >
            <div className={styles.passthrough}>
              {envPassthrough.length > 0 && (
                <div className={styles.passthroughChips}>
                  {envPassthrough.map((n) => (
                    <span key={n} className={styles.chip}>
                      {n}
                      <button
                        type="button"
                        className={styles.remove}
                        aria-label={`Remove ${n}`}
                        onClick={() => removePassthrough(n)}
                      >
                        ✕
                      </button>
                    </span>
                  ))}
                </div>
              )}
              <div className={styles.passthroughAdd}>
                <input
                  id="env-passthrough-input"
                  className={`${input.input} ${input.mono}`}
                  value={passthroughInput}
                  spellCheck={false}
                  onChange={(e) => setPassthroughInput(e.target.value)}
                  onKeyDown={handlePassthroughKeyDown}
                  placeholder="VAR_NAME"
                />
                <Button
                  size="sm"
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

      {showEnvImport && (
        <SettingsSheet
          title="Import from environment"
          width={440}
          onClose={() => setShowEnvImport(false)}
          footer={
            <Button
              variant="primary"
              onClick={() => void handleImport()}
              disabled={selectedCandidates.length === 0}
            >
              Import selected
            </Button>
          }
        >
          <div className={styles.envImport}>
            <p className={styles.sheetLede}>
              Variables set in the environment mads was started from. Only names come back here; values go straight
              from the Router to the vault.
            </p>
            <ul className={styles.candidates}>
              {credentials.envCandidates.map((n) => (
                <li key={n}>
                  <label className={styles.candidate}>
                    <input
                      type="checkbox"
                      checked={selectedCandidates.includes(n)}
                      onChange={() => toggleCandidate(n)}
                    />
                    <span className={styles.name}>{n}</span>
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
          </div>
        </SettingsSheet>
      )}

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
