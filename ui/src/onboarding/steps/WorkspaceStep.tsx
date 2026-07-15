import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { router } from "../../router/client";
import styles from "./WorkspaceStep.module.css";

export interface WorkspaceStepProps {
  onNext: () => void;
  onBack: () => void;
}

/** Step 4/5: name + primary repo path, POST /workspaces (HANDOFF §6). */
export function WorkspaceStep({ onNext, onBack }: WorkspaceStepProps) {
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleCreate = async () => {
    setCreating(true);
    setError(null);
    try {
      await router.createWorkspace({
        name,
        repos: [{ path, primary: true }],
      });
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
        <input
          id="workspace-path"
          className={styles.input}
          value={path}
          onChange={(e) => setPath(e.target.value)}
        />
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
