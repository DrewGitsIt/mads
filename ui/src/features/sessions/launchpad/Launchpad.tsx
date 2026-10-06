import { useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Button } from "../../../components/Button/Button";
import { ContentColumn } from "../../../components/ContentColumn/ContentColumn";
import { Popover } from "../../../components/Popover/Popover";
import { Toast } from "../../../components/Toast/Toast";
import { FLAGS } from "../../../featureFlags";
import { router, type RouterClient } from "../../../router/client";
import type {
  Agent,
  Config,
  CreateSessionPayload,
  Link,
  LinkRequest,
  Session,
  SessionDefaults,
  Workspace,
} from "../../../router/types";
import { LinkChooserDialog } from "../rail/LinkChooserDialog";
import { useProviderMatrix } from "../../settings/state/useProviderMatrix";
import { FolderPill } from "./pills/FolderPill";
import { AgentPill } from "./pills/AgentPill";
import { ModelPill } from "./pills/ModelPill";
import { TrustPill } from "./pills/TrustPill";
import { IsolationPill } from "./pills/IsolationPill";
import type { StagedConfig } from "./pills/stagedConfig";
import { AttachmentChips } from "../../../components/Attachments/AttachmentChips";
import {
  handLaunchpadToSession,
  stagedForLaunchpad,
} from "../../../components/Attachments/stagedAttachments";
import { useStagedAttachments } from "../../../components/Attachments/useStagedAttachments";
import { useFileDrop } from "../../../components/Attachments/useFileDrop";
import { StagedLinkChips } from "./StagedLinkChips";
import { clearLaunchpadDraft, getLaunchpadDraft, setLaunchpadDraft } from "./launchpadDraft";
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

/** Folders past sessions started in, newest first, each once. */
export function recentDirs(sessions: Session[]): string[] {
  const seen = new Set<string>();
  return [...sessions]
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .map((s) => s.dir)
    .filter((d): d is string => !!d && !seen.has(d) && !!seen.add(d));
}

/**
 * Whether the staged pills differ from the user's saved Config defaults —
 * the condition for offering "Save as my defaults". Compared against Config,
 * not the resolved preview: once a pill is staged the preview reports the
 * staged value, which says nothing about what's saved.
 */
export function differsFromSaved(
  staged: StagedConfig,
  workspace: Workspace | undefined,
  cfg: Config,
): boolean {
  if (staged.dir !== undefined && (staged.dir !== cfg.default_dir || !!cfg.default_workspace)) {
    return true;
  }
  if (
    staged.workspace !== undefined &&
    workspace &&
    ((workspace.id !== cfg.default_workspace && workspace.name !== cfg.default_workspace) ||
      !!cfg.default_dir)
  ) {
    return true;
  }
  return (
    (staged.agent !== undefined && staged.agent !== cfg.default_agent) ||
    (staged.provider !== undefined &&
      (staged.provider !== cfg.default_provider || staged.model !== cfg.default_model)) ||
    (staged.trust !== undefined && staged.trust !== cfg.default_trust) ||
    (staged.isolation !== undefined && staged.isolation !== cfg.default_isolation)
  );
}

/** The staged pills written over cfg — what "Save as my defaults" PUTs. */
export function withStagedDefaults(
  cfg: Config,
  staged: StagedConfig,
  workspace: Workspace | undefined,
): Config {
  const next = { ...cfg };
  // Folder and workspace are one pill: saving either clears the other, or a
  // saved workspace's primary repo would silently outrank a saved folder.
  if (staged.dir !== undefined) {
    next.default_dir = staged.dir;
    next.default_workspace = "";
  } else if (staged.workspace !== undefined && workspace) {
    next.default_workspace = workspace.id;
    next.default_dir = "";
  }
  if (staged.agent !== undefined) next.default_agent = staged.agent;
  if (staged.provider !== undefined) {
    next.default_provider = staged.provider;
    next.default_model = staged.model;
  }
  if (staged.trust !== undefined) next.default_trust = staged.trust;
  if (staged.isolation !== undefined) next.default_isolation = staged.isolation;
  return next;
}

export interface LaunchpadProps {
  createSession: (payload: CreateSessionPayload) => Promise<Session>;
  onSessionCreated: (session: Session, goalText: string) => void;
  /** Pickable pool for "Link to session(s)…" (flag-gated) — optional so render sites without the rail's list still compile. */
  sessions?: Session[];
  /** Links touching `sessions`, so the link chooser can show which are already led. */
  links?: Link[];
  /** Defaults to the app's Router client; tests hand in a fake. */
  routerClient?: RouterClient;
}

export function Launchpad({
  createSession,
  onSessionCreated,
  sessions = [],
  links = [],
  routerClient = router,
}: LaunchpadProps) {
  // Seeded from the unsent draft, so leaving for a session and coming back
  // through "+ New session" picks up where the goal was left.
  const [initialDraft] = useState(getLaunchpadDraft);
  const [text, setText] = useState(initialDraft.text);
  const [staged, setStaged] = useState<StagedConfig>(initialDraft.staged);
  const [selectedWorkspace, setSelectedWorkspace] = useState<Workspace | undefined>(
    initialDraft.selectedWorkspace,
  );
  const [selectedAgent, setSelectedAgent] = useState<Agent | undefined>(initialDraft.selectedAgent);
  const [error, setError] = useState<string | null>(null);
  const [resolved, setResolved] = useState<SessionDefaults | undefined>();
  const [savedConfig, setSavedConfig] = useState<Config | undefined>();
  const [savingDefaults, setSavingDefaults] = useState(false);
  const [defaultsSaved, setDefaultsSaved] = useState(false);
  const [stagedLinks, setStagedLinks] = useState<LinkRequest[]>(initialDraft.stagedLinks);
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

  useEffect(() => {
    setLaunchpadDraft({ text, staged, selectedWorkspace, selectedAgent, stagedLinks });
  }, [text, staged, selectedWorkspace, selectedAgent, stagedLinks]);
  // Attached files upload to a draft at once — the session doesn't exist
  // yet — and POST /sessions moves them to it (attachment_draft). Staged
  // outside component state, so they survive navigating away and back.
  const stagedFiles = stagedForLaunchpad(routerClient);
  const attachments = useStagedAttachments(stagedFiles);
  const uploading = attachments.some((a) => a.status === "uploading");
  const { dragging, dropHandlers, onPaste } = useFileDrop((files) => stagedFiles.add(files));
  const { providers } = useProviderMatrix();

  useEffect(() => {
    let cancelled = false;
    routerClient
      .getSessionDefaults(staged.workspace, staged.dir)
      .then((r) => {
        if (!cancelled) setResolved(r);
      })
      .catch(() => {
        if (!cancelled) setResolved(undefined);
      });
    return () => {
      cancelled = true;
    };
    // savedConfig: saving new defaults changes what an unstaged pill resolves to.
  }, [routerClient, staged.workspace, staged.dir, savedConfig]);

  useEffect(() => {
    let cancelled = false;
    routerClient
      .getConfig()
      .then((cfg) => {
        if (!cancelled) setSavedConfig(cfg);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [routerClient]);

  useEffect(() => {
    if (!defaultsSaved) return;
    const t = setTimeout(() => setDefaultsSaved(false), 2500);
    return () => clearTimeout(t);
  }, [defaultsSaved]);

  async function saveDefaults() {
    setSavingDefaults(true);
    try {
      // Re-read rather than PUT the copy from mount: Settings may have
      // changed other fields since, and PUT /config replaces the whole row.
      const current = await routerClient.getConfig();
      const saved = await routerClient.updateConfig(
        withStagedDefaults(current, staged, selectedWorkspace),
      );
      setSavedConfig(saved);
      // The pills now resolve to exactly what was staged, shown as defaults.
      setStaged((prev) => ({ multiRepoPaths: prev.multiRepoPaths }));
      setDefaultsSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSavingDefaults(false);
    }
  }

  async function submit() {
    const trimmed = text.trim();
    if (trimmed === "" || submittingRef.current || stagedFiles.uploading()) return;
    const draft = stagedFiles.owner;
    const hasFiles = stagedFiles.readyIds().length > 0;
    submittingRef.current = true;
    setSubmitting(true);
    try {
      const session = await createSession({
        name: deriveSessionName(trimmed),
        name_source: "derived",
        dir: staged.dir,
        workspace: staged.workspace,
        agent: staged.agent,
        provider: staged.provider,
        model: staged.model,
        trust: staged.trust,
        isolation: staged.isolation,
        // A saved Auto default needs the prompt as much as a picked one.
        ...((staged.agent ?? resolved?.agent?.value) === "auto" ? { opening_prompt: trimmed } : {}),
        ...(stagedLinks.length > 0 ? { links: stagedLinks } : {}),
        ...(hasFiles && "draft" in draft ? { attachment_draft: draft.draft } : {}),
      });
      // Started: the whole draft goes, so the next new session begins from
      // the defaults again. On failure all of it stays — the server rolled
      // back the whole create, so it's still what the user wants to retry.
      setText("");
      setStaged({});
      setSelectedWorkspace(undefined);
      setSelectedAgent(undefined);
      setStagedLinks([]);
      clearLaunchpadDraft();
      // The files are the session's now: its composer's opening auto-send
      // carries them, and the launchpad starts a fresh draft.
      if (hasFiles) handLaunchpadToSession(session.id);
      onSessionCreated(session, trimmed);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  }

  const effectiveIsolation = staged.isolation ?? resolved?.isolation?.value;
  const dirShown = resolved?.dir?.value;
  let dirWarning: string | null = null;
  if (resolved?.dir_status === "missing") {
    dirWarning = `${dirShown} doesn't exist on this machine — choose another folder to start in.`;
  } else if (resolved?.dir_status === "plain" && effectiveIsolation === "worktree") {
    dirWarning = `${dirShown} isn't a git repository, so there's no worktree to isolate this session — it will work in the folder directly.`;
  }
  // Never offer to save a folder that isn't there: every later Start would fail.
  const canSaveDefaults =
    !!savedConfig &&
    resolved?.dir_status !== "missing" &&
    differsFromSaved(staged, selectedWorkspace, savedConfig);

  function handleFilesSelected(e: ChangeEvent<HTMLInputElement>) {
    stagedFiles.add(Array.from(e.target.files ?? []));
    e.target.value = "";
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

        <div
          className={[styles.box, dragging && styles.dropTarget].filter(Boolean).join(" ")}
          {...dropHandlers}
        >
          <textarea
            className={styles.input}
            rows={3}
            aria-label="Describe your goal"
            placeholder="e.g. The deploy script fails on the new Ubuntu runners — help me figure out why"
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={onPaste}
          />
          <AttachmentChips
            items={attachments}
            thumbnailUrl={(a) =>
              a.attachment?.thumbnail
                ? routerClient.attachmentUrl(stagedFiles.owner, a.attachment.id, "thumbnail")
                : undefined
            }
            onRemove={(key) => stagedFiles.remove(key)}
          />
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
              <span className={styles.attachHint}>Or paste or drop them — the agent opens each one</span>
            </div>
            <div className={styles.sendGroup}>
              <span className={styles.hint}>↵ to start · Shift↵ for a new line</span>
              {FLAGS.linkedSessions ? (
                <div className={[styles.splitButton, styles.noWrap].join(" ")}>
                  <Button
                    variant="primary"
                    className={styles.splitMain}
                    disabled={text.trim() === "" || submitting || uploading}
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
                  disabled={text.trim() === "" || submitting || uploading}
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
          <FolderPill
            dir={staged.dir}
            workspace={staged.workspace}
            resolvedDir={resolved?.dir}
            resolvedWorkspace={resolved?.workspace}
            recent={recentDirs(sessions)}
            onPickDir={(dir) => {
              setSelectedWorkspace(undefined);
              setStaged((prev) => ({ ...prev, dir, workspace: undefined }));
            }}
            onPickWorkspace={(ws) => {
              setSelectedWorkspace(ws);
              setStaged((prev) => ({ ...prev, workspace: ws.name, dir: undefined }));
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
            provider={staged.provider ?? resolved?.provider?.value}
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

        {dirWarning && (
          <p className={styles.dirWarning} role="status">
            {dirWarning}
          </p>
        )}

        {defaultsSaved ? (
          <p className={styles.defaultsSaved} role="status">
            Saved as your defaults
          </p>
        ) : (
          canSaveDefaults && (
            <button
              type="button"
              className={styles.saveDefaults}
              disabled={savingDefaults}
              onClick={() => void saveDefaults()}
            >
              Save as my defaults
            </button>
          )
        )}

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
