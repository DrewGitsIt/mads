import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Draft, DraftChip } from "../../components/Draft/Draft";
import type { Agent } from "../../router/types";
import { SimilarAgentWarning } from "./SimilarAgentWarning";
import { useAgentGeneration } from "./useAgentGeneration";
import styles from "./NLGenerateBox.module.css";

export interface NLGenerateBoxProps {
  onApply: (draft: Agent) => void;
}

/** Describe-what-you-want -> POST /agents/generate -> renders the async agent.draft_generated result. */
export function NLGenerateBox({ onApply }: NLGenerateBoxProps) {
  const [description, setDescription] = useState("");
  const { draft, similar, pending, error, generate, clear } = useAgentGeneration();

  return (
    <div className={styles.box}>
      <label className={styles.label} htmlFor="nl-generate-description">
        Describe the agent you want
      </label>
      <textarea
        id="nl-generate-description"
        className={styles.textarea}
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder="e.g. Reviews Go pull requests for race conditions and missing error handling"
      />
      <Button
        variant="primary"
        disabled={!description.trim() || pending}
        onClick={() => void generate({ description })}
      >
        {pending ? "Generating…" : "Generate"}
      </Button>
      {error && <div className={styles.error}>{error}</div>}
      {draft && (
        <div className={styles.result}>
          {similar && <SimilarAgentWarning similar={similar} />}
          <Draft className={styles.draft}>
            <div className={styles.draftHeader}>
              <DraftChip />
              <span>{draft.name}</span>
            </div>
            {draft.description && <p>{draft.description}</p>}
            <pre className={styles.prompt}>{draft.prompt}</pre>
            <div className={styles.actions}>
              <Button variant="primary" onClick={() => onApply(draft)}>
                Use this draft
              </Button>
              <Button variant="ghost" onClick={clear}>
                Discard
              </Button>
            </div>
          </Draft>
        </div>
      )}
    </div>
  );
}
