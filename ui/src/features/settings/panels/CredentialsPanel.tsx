import { useState, type FormEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { useCredentials } from "../state/useCredentials";
import styles from "./CredentialsPanel.module.css";

/**
 * Masked credential list + add/delete form + env-import picker + vault-sources
 * empty state (HANDOFF §8, the security acceptance criterion). The value
 * input is cleared synchronously in this component's own submit handler,
 * before add()'s network call is awaited — never in a .then().
 */
export function CredentialsPanel() {
  const credentials = useCredentials();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [selectedCandidates, setSelectedCandidates] = useState<string[]>([]);
  const [showEnvImport, setShowEnvImport] = useState(false);

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

  return (
    <div className={styles.panel}>
      <section>
        <h3 className={styles.heading}>Credentials</h3>
        <ul className={styles.list}>
          {credentials.names.map((n) => (
            <li key={n} className={styles.row}>
              <span className={styles.name}>{n}</span>
              <span className={styles.masked} aria-hidden="true">
                ••••••••
              </span>
              <Button size="sm" variant="ghost" onClick={() => void credentials.remove(n)}>
                Delete
              </Button>
            </li>
          ))}
          {credentials.names.length === 0 && <li className={styles.empty}>No credentials stored.</li>}
        </ul>
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
                <li className={styles.empty}>No unimported env vars detected.</li>
              )}
            </ul>
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
    </div>
  );
}
