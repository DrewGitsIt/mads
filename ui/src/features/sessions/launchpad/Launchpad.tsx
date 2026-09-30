import { useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { Popover } from "../../../components/Popover/Popover";
import { Toast } from "../../../components/Toast/Toast";
import { FLAGS } from "../../../featureFlags";
import { router } from "../../../router/client";
import type {
  Agent,
  CreateSessionPayload,
  Link,
  LinkRequest,
  Session,
  SessionDefaults,
  Workspace,
} from "../../../router/types";
import { LinkChooserDialog } from "../rail/LinkChooserDialog";
import { useProviderMatrix } from "../../settings/state/useProviderMatrix";
import { WorkspacePill } from "./pills/WorkspacePill";
import { AgentPill } from "./pills/AgentPill";
import { ModelPill } from "./pills/ModelPill";
import { TrustPill } from "./pills/TrustPill";
import { IsolationPill } from "./pills/IsolationPill";
import type { StagedConfig } from "./pills/stagedConfig";
import { AttachmentChips } from "./AttachmentChips";
import { StagedLinkChips } from "./StagedLinkChips";
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
  createSession: (payload: CreateSessionPayload) => Promise<Session>;
  onSessionCreated: (session: Session, goalText: string) => void;
  /** Pickable pool for "Link to session(s)…" (flag-gated) — optional so render sites without the rail's list still compile. */
  sessions?: Session[];
  /** Links touching `sessions`, so the link chooser can show which are already led. */
  links?: Link[];
}

export function Launchpad({
  createSession,
  onSessionCreated,
  sessions = [],
  links = [],
}: LaunchpadProps) {
  const [text, setText] = useState("");
  const [staged, setStaged] = useState<StagedConfig>({});
  const [selectedWorkspace, setSelectedWorkspace] = useState<Workspace | undefined>();
  const [selectedAgent, setSelectedAgent] = useState<Agent | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [resolved, setResolved] = useState<SessionDefaults | undefined>();
  const [attachments, setAttachments] = useState<AttachedFile[]>([]);
  const [stagedLinks, setStagedLinks] = useState<LinkRequest[]>([]);
  const [menuOpen, setMenuOpen] = useState(false);
  const [linkDialogOpen, setLinkDialogOpen] = useState(false);
  // In-flight create guard. Creating a session is slow by construction
  // (agent auto-selection runs Fast-Model calls inside the request, then the
  // child spawn waits for a readiness handshake) — without this, every Enter
  // press or button click in that multi-second window fires another real
  // POST /sessions (the incident: one goal, three sessions). The ref is the
  // synchronous arbiter (state updates don't land within a same-tick double
  // keypress); the state drives the disabled button and "Starting…" label.
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
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
    if (trimmed === "" || submittingRef.current) return;
    submittingRef.current = true;
    setSubmitting(true);
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
        ...(staged.agent === "auto" ? { opening_prompt: trimmed } : {}),
        ...(stagedLinks.length > 0 ? { links: stagedLinks } : {}),
      });
      setText("");
      setAttachments([]);
      // On failure the chips stay: the server rolled back the whole create,
      // so the staged links are still what the user wants to retry with.
      setStagedLinks([]);
      onSessionCreated(session, buildGoalText(trimmed, attachments));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
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
      <ContentColumn variant="narrow" className={styles.hero}>
        <div className={styles.header}>
          <h2 className={styles.title}>What can I help you with?</h2>
          <p className={styles.subtitle}>Describe your goal — I&rsquo;ll help you get there.</p>
        </div>

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
                variant="secondary"
                size="sm"
                className={styles.noWrap}
                onClick={() => fileInputRef.current?.click()}
              >
                <span aria-hidden="true">📎</span> Attach files
              </Button>
              <span className={styles.attachHint}>Files are shared with the agent automatically</span>
            </div>
            <div className={styles.sendGroup}>
              <span className={styles.hint}>↵ to start · Shift↵ for a new line</span>
              {FLAGS.linkedSessions ? (
                <div className={[styles.splitButton, styles.noWrap].join(" ")}>
                  <Button
                    variant="primary"
                    className={styles.splitMain}
                    disabled={text.trim() === "" || submitting}
                    onClick={() => void submit()}
                  >
                    {submitting ? "Starting…" : stagedLinks.length > 0 ? "Start & link" : "Start session"}
                  </Button>
                  <Popover
                    open={menuOpen}
                    onClose={() => setMenuOpen(false)}
                    className={styles.startMenu}
                    anchor={
                      <Button
                        variant="primary"
                        className={styles.splitArrow}
                        aria-label="More start options"
                        aria-haspopup="menu"
                        aria-expanded={menuOpen}
                        onClick={() => setMenuOpen((open) => !open)}
                      >
                        ▾
                      </Button>
                    }
                  >
                    <button
                      type="button"
                      role="menuitem"
                      className={styles.menuItem}
                      disabled={sessions.length === 0}
                      onClick={() => {
                        // Close the menu before opening the dialog — both
                        // listen for document-level Escape; don't stack them.
                        setMenuOpen(false);
                        setLinkDialogOpen(true);
                      }}
                    >
                      Link to session(s)…
                      {sessions.length === 0 && (
                        <span className={styles.menuHint}>No other sessions to link yet</span>
                      )}
                    </button>
                  </Popover>
                </div>
              ) : (
                <Button
                  variant="primary"
                  className={styles.noWrap}
                  disabled={text.trim() === "" || submitting}
                  onClick={() => void submit()}
                >
                  {submitting ? "Starting…" : "Start session"}
                </Button>
              )}
            </div>
          </div>
          <StagedLinkChips
            links={stagedLinks}
            sessions={sessions}
            onRemove={(subjectId) =>
              setStagedLinks((prev) => prev.filter((l) => l.subject_session_id !== subjectId))
            }
          />
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
          {/* Redesign §4/§6: the persona is an *override*, not a *selection* —
           *  default is Auto (server-side Fast Model pick over the persona
           *  registry). A concrete persona override, selected from the
           *  Library's Personas shelf, is honored and replaces the pick. */}
          <AgentPill
            value={staged.agent}
            resolved={resolved?.agent}
            onChange={(agent) => {
              setSelectedAgent(agent);
              setStaged((prev) => ({ ...prev, agent: agent.id }));
            }}
            onAuto={() => {
              setSelectedAgent(undefined);
              setStaged((prev) => ({ ...prev, agent: "auto" }));
            }}
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
            agentPermissions={selectedAgent?.permissions}
            intentMatrix={
              providers.find((p) => p.name === (staged.provider ?? resolved?.provider?.value))
                ?.intents
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

        {FLAGS.linkedSessions && linkDialogOpen && (
          <LinkChooserDialog
            sessionIds={[]}
            sessions={sessions}
            links={links}
            routerClient={router}
            stagedHolder={{
              label: "This session (not started yet)",
              onStage: (requests) => {
                setStagedLinks(requests);
                setLinkDialogOpen(false);
              },
            }}
            initialSubjectIds={stagedLinks.map((l) => l.subject_session_id)}
            initialPreset={
              stagedLinks[0]?.preset === "monitor" || stagedLinks[0]?.preset === "leader"
                ? stagedLinks[0].preset
                : undefined
            }
            onClose={() => setLinkDialogOpen(false)}
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
