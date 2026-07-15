import { useState } from "react";
import { Button } from "../../components/Button/Button";
import type { AgentCapabilities } from "../../router/types";
import { useAgentEnrichment } from "./useAgentEnrichment";
import styles from "./EnrichmentActions.module.css";

export interface EnrichmentActionsProps {
  prompt: string;
  onDescribe: (description: string) => void;
  onSuggestCapabilities: (capabilities: AgentCapabilities) => void;
  onRefine: (prompt: string) => void;
}

/** Describe / Suggest capabilities / Refine — synchronous enrichment actions (spec §9.5). */
export function EnrichmentActions({
  prompt,
  onDescribe,
  onSuggestCapabilities,
  onRefine,
}: EnrichmentActionsProps) {
  const { describe, suggestCapabilities, refine } = useAgentEnrichment();
  const [instruction, setInstruction] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run<T>(name: string, fn: () => Promise<T>, apply: (result: T) => void) {
    setBusy(name);
    setError(null);
    try {
      apply(await fn());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.actions}>
      <div className={styles.buttons}>
        <Button
          size="sm"
          disabled={!prompt.trim() || busy !== null}
          onClick={() => void run("describe", () => describe(prompt), onDescribe)}
        >
          {busy === "describe" ? "Describing…" : "Describe"}
        </Button>
        <Button
          size="sm"
          disabled={!prompt.trim() || busy !== null}
          onClick={() =>
            void run("suggest", () => suggestCapabilities(prompt), onSuggestCapabilities)
          }
        >
          {busy === "suggest" ? "Suggesting…" : "Suggest capabilities"}
        </Button>
      </div>
      <div className={styles.refineRow}>
        <input
          className={styles.instruction}
          placeholder="Refine instruction (e.g. make it more concise)"
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
        />
        <Button
          size="sm"
          disabled={!prompt.trim() || !instruction.trim() || busy !== null}
          onClick={() => void run("refine", () => refine(prompt, instruction), onRefine)}
        >
          {busy === "refine" ? "Refining…" : "Refine"}
        </Button>
      </div>
      {error && <div className={styles.error}>{error}</div>}
    </div>
  );
}
