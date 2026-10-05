import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { PathField } from "../../components/PathField/PathField";
import { router } from "../../router/client";
import type { Provider } from "../../router/types";
import type { UseConfig } from "../../features/settings/state/useConfig";
import styles from "./WorkspaceStep.module.css";

export interface WorkspaceStepProps {
  onNext: () => void;
  onBack: () => void;
  config: UseConfig;
}

/**
 * Step 4/5: name + primary repo path, POST /workspaces, plus an optional
 * default-provider select (HANDOFF §6). The created workspace id and the
 * chosen provider (if any) are staged into the shared `config` draft so
 * FirstRunWizard's final `PUT /config` persists them as
 * default_workspace/default_provider.
 */
export function WorkspaceStep({ onNext, onBack, config }: WorkspaceStepProps) {
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [provider, setProvider] = useState("");
  const [providers, setProviders] = useState<Provider[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void router.listProviderNames().then(setProviders).catch(() => {});
  }, []);

  const handleCreate = async () => {
    setCreating(true);
    setError(null);
    try {
      const created = await router.createWorkspace({
        name,
        repos: [{ path, primary: true }],
      });
      if (config.draft) {
        config.setDraft({
          ...config.draft,
          default_workspace: created.id,
          ...(provider ? { default_provider: provider } : {}),
        });
      }
      onNext();
    } catch {
      setError("Couldn't create the workspace. Check the path and try again.");
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className={styles.step}>
      <h2 className={styles.title}>First workspace</h2>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="workspace-name">
          Name
        </label>
        <input
          id="workspace-name"
          className={styles.input}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="workspace-path">
          Primary repo path
        </label>
        <PathField
          id="workspace-path"
          className={styles.input}
          dialogTitle="Choose the primary repo"
          placeholder="~/repo/my-project"
          value={path}
          onChange={setPath}
        />
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor="workspace-provider">
          Default provider
        </label>
        <select
          id="workspace-provider"
          className={styles.input}
          value={provider}
          onChange={(e) => setProvider(e.target.value)}
        >
          <option value="">No default</option>
          {providers.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </div>
      {error && <div className={styles.error}>{error}</div>}
      <div className={styles.actions}>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button
          variant="primary"
          onClick={() => void handleCreate()}
          disabled={!name.trim() || !path.trim() || creating}
        >
          Create workspace
        </Button>
      </div>
    </div>
  );
}
