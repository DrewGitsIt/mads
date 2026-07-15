import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Panel, PanelBody, PanelHeader } from "../../components/Panel/Panel";
import { router } from "../../router/client";
import type { Capability, DiagnoseResult } from "../../router/types";
import styles from "./DiagnosePanel.module.css";

export interface DiagnosePanelProps {
  capability: Capability;
}

/** POST /capabilities/{id}/diagnose — one-off health check + suggestions. */
export function DiagnosePanel({ capability }: DiagnosePanelProps) {
  const [result, setResult] = useState<DiagnoseResult | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isLocalMcp = capability.type === "mcp" && !capability.config.url;

  async function run() {
    setPending(true);
    setError(null);
    try {
      const diagnosis = await router.diagnoseCapability(capability.id);
      setResult(diagnosis);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <Panel>
      <PanelHeader>Diagnose</PanelHeader>
      <PanelBody>
        <Button variant="secondary" disabled={pending} onClick={() => void run()}>
          {pending ? "Checking…" : "Run diagnose"}
        </Button>
        {error && <div className={styles.error}>{error}</div>}
        {result && (
          <div className={styles.result}>
            <div className={result.healthy ? styles.healthy : styles.unhealthy}>
              {result.healthy ? "Healthy" : (result.reason ?? "Unhealthy")}
            </div>
            {result.suggestions.length > 0 && (
              <ul className={styles.suggestions}>
                {result.suggestions.map((suggestion) => (
                  <li key={suggestion}>{suggestion}</li>
                ))}
              </ul>
            )}
            {isLocalMcp && (
              <p className={styles.caveat}>
                Based on configuration, not a live check — this MCP server has no URL to reach directly.
              </p>
            )}
          </div>
        )}
      </PanelBody>
    </Panel>
  );
}
