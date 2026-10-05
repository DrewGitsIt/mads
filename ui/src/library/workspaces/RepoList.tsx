import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { PathField } from "../../components/PathField/PathField";
import type { WorkspaceRepo } from "../../router/types";
import styles from "./RepoList.module.css";

export interface RepoListProps {
  repos: WorkspaceRepo[];
  onChange: (repos: WorkspaceRepo[]) => void;
}

/** Repo path list + primary-repo picker for a workspace's WorkspaceRepo[]. */
export function RepoList({ repos, onChange }: RepoListProps) {
  const [draft, setDraft] = useState("");

  function add(raw: string = draft) {
    const path = raw.trim();
    if (!path || repos.some((r) => r.path === path)) return;
    onChange([...repos, { path, primary: repos.length === 0 }]);
    setDraft("");
  }

  function remove(path: string) {
    onChange(repos.filter((r) => r.path !== path));
  }

  function setPrimary(path: string) {
    onChange(repos.map((r) => ({ ...r, primary: r.path === path })));
  }

  return (
    <div className={styles.field}>
      <span className={styles.label}>Repos</span>
      <ul className={styles.list}>
        {repos.map((repo) => (
          <li key={repo.path} className={styles.row}>
            <label className={styles.primary}>
              <input
                type="radio"
                name="primary-repo"
                checked={!!repo.primary}
                onChange={() => setPrimary(repo.path)}
              />
              Primary
            </label>
            <span className={styles.path}>{repo.path}</span>
            <Button variant="ghost" size="sm" onClick={() => remove(repo.path)}>
              Remove
            </Button>
          </li>
        ))}
      </ul>
      <div className={styles.add}>
        <PathField
          aria-label="Repo path"
          value={draft}
          onChange={setDraft}
          // Choosing a folder in the dialog is the whole act — add it.
          onPick={add}
          dialogTitle="Add a repo"
          placeholder="e.g. ~/repo/mads"
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button variant="secondary" size="sm" onClick={() => add()}>
          Add repo
        </Button>
      </div>
    </div>
  );
}
