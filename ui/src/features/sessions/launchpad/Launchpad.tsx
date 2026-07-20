import { useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { Toast } from "../../../components/Toast/Toast";
import { router } from "../../../router/client";
import type { Session, SessionDefaults, Workspace } from "../../../router/types";
import { useProviderMatrix } from "../../settings/state/useProviderMatrix";
import { WorkspacePill } from "./pills/WorkspacePill";
import { AgentPill } from "./pills/AgentPill";
import { ModelPill } from "./pills/ModelPill";
import { TrustPill } from "./pills/TrustPill";
import { IsolationPill } from "./pills/IsolationPill";
import type { StagedConfig } from "./pills/stagedConfig";
import { SuggestionChips } from "./SuggestionChips";
import { AttachmentChips } from "./AttachmentChips";
import { buildGoalText, toAttachedFile, type AttachedFile } from "./attachments";
import styles from "./Launchpad.module.css";

/**
 * Backend requires a non-empty Session.Name with no server-side derivation —
 * derive one from the first line of the composer text, stored in full. The
 * header and rail truncate long names with CSS, not by cutting the stored
 * string; the router job (services/router/internal/job/title.go) replaces
 * this placeholder with a generated title after the session's first turn.
 */
export function deriveSessionName(text: string): string {
  return text.trim().split("\n")[0];
}

export interface LaunchpadProps {
  createSession: (payload: Partial<Session>) => Promise<Session>;
  onSessionCreated: (session: Session, goalText: string) => void;
}

export function Launchpad({ createSession, onSessionCreated }: LaunchpadProps) {
  const [text, setText] = useState("");
  const [staged, setStaged] = useState<StagedConfig>({});
  const [selectedWorkspace, setSelectedWorkspace] = useState<Workspace | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [resolved, setResolved] = useState<SessionDefaults | undefined>();
  const [attachments, setAttachments] = useState<AttachedFile[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const { providers } = useProviderMatrix();

  useEffect(() => {
    let cancelled = false;
    router
      .getSessionDefaults(staged.workspace)
      .then((r) => {
        if (!cancelled) setResolved(r);
      })
      .catch(() => {
        if (!cancelled) setResolved(undefined);
      });
    return () => {
      cancelled = true;
    };
  }, [staged.workspace]);

  async function submit() {
    const trimmed = text.trim();
    if (trimmed === "") return;
    try {
      const session = await createSession({
        name: deriveSessionName(trimmed),
        name_source: "derived",
        workspace: staged.workspace,
        agent: staged.agent,
        provider: staged.provider,
        model: staged.model,
        trust: staged.trust,
        isolation: staged.isolation,
      });
      setText("");
      setAttachments([]);
      onSessionCreated(session, buildGoalText(trimmed, attachments));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function handleFilesSelected(e: ChangeEvent<HTMLInputElement>) {
    const files = e.target.files;
    if (files) {
      setAttachments((prev) => [...prev, ...Array.from(files).map(toAttachedFile)]);
    }
    e.target.value = "";
  }

  function removeAttachment(id: string) {
    setAttachments((prev) => {
      const removed = prev.find((f) => f.id === id);
      if (removed?.previewUrl) URL.revokeObjectURL(removed.previewUrl);
      return prev.filter((f) => f.id !== id);
    });
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
          <AttachmentChips files={attachments} onRemove={removeAttachment} />
          <div className={styles.actions}>
            <div className={styles.attachGroup}>
              <input
                ref={fileInputRef}
                type="file"
                multiple
                aria-label="Attach files"
                className={styles.fileInput}
                onChange={handleFilesSelected}
              />
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => fileInputRef.current?.click()}
              >
                Attach files
              </Button>
              <span className={styles.attachHint}>Files are shared with the agent automatically</span>
            </div>
            <div className={styles.sendGroup}>
              <span className={styles.hint}>↵ to start · Shift↵ for a new line</span>
              <Button variant="primary" disabled={text.trim() === ""} onClick={() => void submit()}>
                Start session
              </Button>
            </div>
          </div>
        </div>

        <div className={styles.pills}>
          <WorkspacePill
            value={staged.workspace}
            resolved={resolved?.workspace}
            onChange={(ws) => {
              setSelectedWorkspace(ws);
              setStaged((prev) => ({ ...prev, workspace: ws.name }));
            }}
          />
          <AgentPill
            value={staged.agent}
            resolved={resolved?.agent}
            onChange={(agent) => setStaged((prev) => ({ ...prev, agent: agent.name }))}
          />
          <ModelPill
            provider={staged.provider}
            model={staged.model}
            resolved={resolved?.model}
            resolvedProvider={resolved?.provider?.value}
            onChange={(provider, model) =>
              setStaged((prev) => ({ ...prev, provider, model: model.id }))
            }
          />
          <TrustPill
            value={staged.trust}
            resolved={resolved?.trust}
            matrix={
              providers.find((p) => p.name === (staged.provider ?? resolved?.provider?.value))
                ?.matrix
            }
            onChange={(trust) => setStaged((prev) => ({ ...prev, trust }))}
          />
          <IsolationPill
            value={staged.isolation}
            resolved={resolved?.isolation}
            onChange={(isolation) => setStaged((prev) => ({ ...prev, isolation }))}
            repos={selectedWorkspace?.repos}
            selectedRepoPaths={staged.multiRepoPaths}
            onRepoPathsChange={(paths) => setStaged((prev) => ({ ...prev, multiRepoPaths: paths }))}
          />
        </div>

        {resolved?.workspace &&
          [resolved.agent, resolved.model, resolved.isolation].some(
            (f) => f.source === "workspace",
          ) && <p className={styles.inheritLine}>Inherits from {resolved.workspace} defaults</p>}

        <SuggestionChips onPick={(suggestionText) => setText(suggestionText)} />

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
