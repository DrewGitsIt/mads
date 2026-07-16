import { useState, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { Toast } from "../../../components/Toast/Toast";
import type { Session, Workspace } from "../../../router/types";
import { WorkspacePill } from "./pills/WorkspacePill";
import { AgentPill } from "./pills/AgentPill";
import { ModelPill } from "./pills/ModelPill";
import { TrustPill } from "./pills/TrustPill";
import { IsolationPill } from "./pills/IsolationPill";
import type { StagedConfig } from "./pills/stagedConfig";
import { SuggestionChips } from "./SuggestionChips";
import { ProvisioningStrip } from "./ProvisioningStrip";
import styles from "./Launchpad.module.css";

const MAX_NAME_LENGTH = 60;

/** Backend requires a non-empty Session.Name with no server-side derivation — derive one from the first line of the composer text. */
export function deriveSessionName(text: string): string {
  const firstLine = text.trim().split("\n")[0];
  return firstLine.length > MAX_NAME_LENGTH
    ? `${firstLine.slice(0, MAX_NAME_LENGTH)}…`
    : firstLine;
}

export interface LaunchpadProps {
  createSession: (payload: Partial<Session>) => Promise<Session>;
}

export function Launchpad({ createSession }: LaunchpadProps) {
  const [text, setText] = useState("");
  const [staged, setStaged] = useState<StagedConfig>({});
  const [selectedWorkspace, setSelectedWorkspace] = useState<Workspace | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [provisioningSessionId, setProvisioningSessionId] = useState<string | null>(null);

  async function submit() {
    const trimmed = text.trim();
    if (trimmed === "") return;
    try {
      const session = await createSession({
        name: deriveSessionName(trimmed),
        workspace: staged.workspace,
        agent: staged.agent,
        provider: staged.provider,
        model: staged.model,
        trust: staged.trust,
        isolation: staged.isolation,
      });
      setProvisioningSessionId(session.id);
      setText("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void submit();
    }
  }

  return (
    <div className={styles.launchpad}>
      <ContentColumn variant="narrow">
        <h2 className={styles.title}>What can I help you with?</h2>
        <p className={styles.subtitle}>Describe your goal — I&rsquo;ll help you get there.</p>

        <div className={styles.box}>
          <textarea
            className={styles.input}
            rows={3}
            aria-label="Describe your goal"
            placeholder="e.g. The deploy script fails on the new Ubuntu runners — help me figure out why"
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
          />
          <div className={styles.actions}>
            <span className={styles.hint}>↵ to start · Shift↵ for a new line</span>
            <Button variant="primary" disabled={text.trim() === ""} onClick={() => void submit()}>
              Start session
            </Button>
          </div>
        </div>

        <div className={styles.pills}>
          <WorkspacePill
            value={staged.workspace}
            onChange={(ws) => {
              setSelectedWorkspace(ws);
              setStaged((prev) => ({
                ...prev,
                workspace: ws.name,
                agent: prev.agent ?? ws.defaults.agent,
                model: prev.model ?? ws.defaults.model,
                isolation: prev.isolation ?? ws.defaults.isolation,
              }));
            }}
          />
          <AgentPill
            value={staged.agent}
            onChange={(agent) => setStaged((prev) => ({ ...prev, agent: agent.name }))}
          />
          <ModelPill
            provider={staged.provider}
            model={staged.model}
            onChange={(provider, model) =>
              setStaged((prev) => ({ ...prev, provider, model: model.id }))
            }
          />
          <TrustPill
            value={staged.trust}
            onChange={(trust) => setStaged((prev) => ({ ...prev, trust }))}
          />
          <IsolationPill
            value={staged.isolation}
            onChange={(isolation) => setStaged((prev) => ({ ...prev, isolation }))}
            repos={selectedWorkspace?.repos}
            selectedRepoPaths={staged.multiRepoPaths}
            onRepoPathsChange={(paths) => setStaged((prev) => ({ ...prev, multiRepoPaths: paths }))}
          />
        </div>

        <SuggestionChips onPick={(suggestionText) => setText(suggestionText)} />

        {provisioningSessionId && (
          <ProvisioningStrip
            sessionId={provisioningSessionId}
            onDone={() => setProvisioningSessionId(null)}
          />
        )}

        {error && (
          <Toast
            tone="bad"
            title="Couldn't start session"
            description={error}
            onDismiss={() => setError(null)}
          />
        )}
      </ContentColumn>
    </div>
  );
}
