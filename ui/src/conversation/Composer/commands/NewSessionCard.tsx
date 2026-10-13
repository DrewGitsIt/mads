import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { requestOpenSession } from "../../../appNavigation";
import { Button } from "../../../components/Button/Button";
import { PopoverPlacementContext } from "../../../components/Popover/Popover";
import { SegmentedControl } from "../../../components/SegmentedControl/SegmentedControl";
import { buildCreateSessionPayload } from "../../../features/sessions/launchpad/sessionPayload";
import { AgentPill } from "../../../features/sessions/pills/AgentPill";
import { FolderPill } from "../../../features/sessions/pills/FolderPill";
import { IsolationPill } from "../../../features/sessions/pills/IsolationPill";
import { ModelPill } from "../../../features/sessions/pills/ModelPill";
import { TrustPill } from "../../../features/sessions/pills/TrustPill";
import type { StagedConfig } from "../../../features/sessions/pills/stagedConfig";
import { useProviderMatrix } from "../../../features/settings/state/useProviderMatrix";
import { formatShortcut } from "../../../platform/shortcut";
import type { RouterClient } from "../../../router/client";
import type {
  Agent,
  CreateSessionPayload,
  LinkCadence,
  Session,
  SessionValidation,
} from "../../../router/types";
import { DEFAULT_CADENCE, stagedFromSession, stagedLinkFor, type NewVariant } from "./newSessionLinks";
import styles from "./NewSessionCard.module.css";

/** Why the card can't start at all — /new-leader forced past its disabled menu row. */
export interface NewSessionBlock {
  /** The menu row's short reason. */
  reason: string;
  /** The card's banner. */
  message: string;
  /** The current leader, for the banner's "Open <name>". */
  leader?: { id: string; name: string };
}

export interface NewSessionCardProps {
  variant: NewVariant;
  /** This session — the pills' starting values and the relationship row's name. */
  session?: Session;
  sessionId: string;
  routerClient: RouterClient;
  initialGoal: string;
  block?: NewSessionBlock;
  /** Creates the session; `open` is ⌘↵. Rejecting keeps the card up with the message. */
  onStart: (payload: CreateSessionPayload, goal: string, open: boolean) => Promise<void>;
  /** Debounce for the dry-run validation; tests pass 0. */
  validateDelayMs?: number;
}

const CADENCES: { value: LinkCadence; label: string }[] = [
  { value: "on_signal", label: "on signal" },
  { value: "per_turn", label: "per turn" },
  { value: "manual_only", label: "manual" },
];

/**
 * The relationship row's words: who holds the link over whom, and the
 * tooltips that say what that allows. Linked variants only.
 */
function relationship(variant: NewVariant, thisName: string) {
  const NEW = "the new session";
  switch (variant) {
    case "leader":
      return {
        newHolds: true,
        verb: "leads",
        allows: `The new session will lead ${thisName}. It can read what ${thisName} is doing, send it messages, and direct its work through its orchestrate tools.`,
        holder: NEW,
        subject: thisName,
      };
    case "follower":
      return {
        newHolds: false,
        verb: "leads",
        allows: `${thisName} will lead the new session. It can read what the new session is doing, send it messages, and direct its work through its orchestrate tools.`,
        holder: thisName,
        subject: NEW,
      };
    case "monitor":
      return {
        newHolds: true,
        verb: "watches",
        allows: `The new session will watch ${thisName}. It gets a digest of ${thisName}'s activity but can't send it messages.`,
        holder: NEW,
        subject: thisName,
      };
    default:
      return undefined;
  }
}

/**
 * The /new card (docs/mockups/session-commands.html §2): no header — for a
 * linked variant the relationship row with its cadence switch, then the
 * goal, the Launchpad's pills (starting at this session's values), any
 * banners, and Start ↵ / Start & open ⌘↵. Every pill or cadence change is
 * dry-run against POST /sessions/validate (debounced): a joined counterpart
 * worktree locks Isolation, and any error the create would hit disables
 * both Starts with the server's message.
 */
export function NewSessionCard({
  variant,
  session,
  sessionId,
  routerClient,
  initialGoal,
  block,
  onStart,
  validateDelayMs = 250,
}: NewSessionCardProps) {
  const [goal, setGoal] = useState(initialGoal);
  // The session's own values: the starting point, and what "changed" (a
  // pill's accent) is measured against.
  const [base] = useState<StagedConfig>(() => stagedFromSession(session));
  const [staged, setStaged] = useState<StagedConfig>(base);
  const [selectedAgent, setSelectedAgent] = useState<Agent>();
  const [cadence, setCadence] = useState<LinkCadence>(DEFAULT_CADENCE);
  const [validation, setValidation] = useState<SessionValidation>();
  const [submitting, setSubmitting] = useState(false);
  const [startError, setStartError] = useState<string>();
  const submittingRef = useRef(false);
  const goalRef = useRef(goal);
  goalRef.current = goal;
  const cadenceLabelId = useId();
  const { providers } = useProviderMatrix();

  const thisName = session?.name || "This session";
  const rel = relationship(variant, thisName);

  function payloadFor(goalText: string): CreateSessionPayload {
    const link = stagedLinkFor(variant, sessionId, cadence);
    return buildCreateSessionPayload(goalText, staged, { links: link ? [link] : [] });
  }

  // Every variant is dry-run, not only the linked ones: it costs no model
  // call, and a plain /new still gets the server's verdict on its folder and
  // provider before Start rather than as a failed create. The goal isn't a
  // dependency — it only names the session.
  useEffect(() => {
    let cancelled = false;
    const t = setTimeout(() => {
      routerClient
        .validateSession(payloadFor(goalRef.current))
        .then((v) => {
          if (!cancelled) setValidation(v);
        })
        .catch(() => {
          // Couldn't ask: say nothing — create still runs every check.
          if (!cancelled) setValidation(undefined);
        });
    }, validateDelayMs);
    return () => {
      cancelled = true;
      clearTimeout(t);
    };
    // payloadFor reads exactly these.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routerClient, sessionId, variant, staged, cadence, validateDelayMs]);

  const joined = validation?.joined_worktree_of;
  // The blocked banner already says why a forced /new-leader can't go;
  // the server's own link refusal would repeat it.
  const errors = (validation?.errors ?? []).filter(
    (e) => !(block && e.field.startsWith("links")),
  );
  const canStart = goal.trim() !== "" && !block && errors.length === 0 && !submitting;

  async function start(open: boolean) {
    if (!canStart || submittingRef.current) return;
    submittingRef.current = true;
    setSubmitting(true);
    setStartError(undefined);
    try {
      await onStart(payloadFor(goal), goal.trim(), open);
    } catch (err) {
      setStartError(err instanceof Error ? err.message : String(err));
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  }

  function handleKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      void start(true);
    } else if (!e.shiftKey && e.target instanceof HTMLTextAreaElement) {
      // Plain ↵ starts only from the goal field — elsewhere it presses
      // whatever has focus (a pill, a menu row).
      e.preventDefault();
      void start(false);
    }
  }

  const providerName = staged.provider;
  const providerInfo = providers.find((p) => p.name === providerName);

  return (
    <PopoverPlacementContext.Provider value="above">
      <div className={styles.card} onKeyDown={handleKeyDown} data-testid="new-session-card">
        {rel && (
          <div className={styles.linkviz}>
            {rel.newHolds ? <NewNode /> : <ThisNode name={thisName} />}
            <span className={styles.arrow} title={rel.allows} tabIndex={0} aria-label={rel.allows}>
              ── {rel.verb} ──▶
            </span>
            {rel.newHolds ? <ThisNode name={thisName} /> : <NewNode />}
            <span className={styles.cadence}>
              <span
                id={cadenceLabelId}
                className={styles.cadenceLabel}
                title={`How often ${rel.holder} gets a digest of ${rel.subject}'s activity: on signal = when ${rel.subject} finishes or needs attention; per turn = after every turn; manual = only when asked.`}
              >
                Cadence
              </span>
              <SegmentedControl
                aria-labelledby={cadenceLabelId}
                value={cadence}
                options={CADENCES}
                onChange={setCadence}
                className={styles.seg}
              />
            </span>
          </div>
        )}

        <textarea
          className={styles.goal}
          rows={2}
          aria-label="Goal for the new session"
          placeholder="What should the new session do?"
          value={goal}
          autoFocus
          onFocus={(e) => {
            const end = e.currentTarget.value.length;
            e.currentTarget.setSelectionRange(end, end);
          }}
          onChange={(e) => setGoal(e.target.value)}
        />

        <div className={styles.pills}>
          <FolderPill
            modified={staged.dir !== base.dir || staged.workspace !== base.workspace}
            dir={staged.dir}
            workspace={staged.workspace}
            recent={session?.dir ? [session.dir] : []}
            onPickDir={(dir) => setStaged((prev) => ({ ...prev, dir, workspace: undefined }))}
            onPickWorkspace={(ws) =>
              setStaged((prev) => ({ ...prev, workspace: ws.name, dir: undefined }))
            }
          />
          <AgentPill
            modified={staged.agent !== base.agent}
            value={staged.agent}
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
            modified={staged.provider !== base.provider || staged.model !== base.model}
            provider={staged.provider}
            model={staged.model}
            onChange={(provider, model) =>
              setStaged((prev) => ({ ...prev, provider, model: model.id }))
            }
          />
          <TrustPill
            modified={staged.trust !== base.trust}
            value={staged.trust}
            matrix={providerInfo?.matrix}
            provider={providerName}
            agentPermissions={selectedAgent?.permissions}
            intentMatrix={providerInfo?.intents}
            onChange={(trust) => setStaged((prev) => ({ ...prev, trust }))}
          />
          <IsolationPill
            modified={staged.isolation !== base.isolation}
            value={staged.isolation}
            onChange={(isolation) => setStaged((prev) => ({ ...prev, isolation }))}
            locked={
              joined
                ? {
                    label: `shares ${joined.name}'s worktree`,
                    reason: `${joined.name} works in its own worktree of this repo. Linked sessions must share one copy of the repo, so the new session works inside ${joined.name}'s worktree.`,
                  }
                : undefined
            }
          />
        </div>

        {block && (
          <div className={[styles.banner, styles.bad].join(" ")} role="alert">
            <span>✕ {block.message}</span>
            {block.leader && (
              <button
                type="button"
                className={styles.bannerLink}
                onClick={() => requestOpenSession(block.leader!.id)}
              >
                Open {block.leader.name}
              </button>
            )}
          </div>
        )}
        {joined && (
          <div className={[styles.banner, styles.info].join(" ")} role="status">
            ⓘ {joined.name} has its own worktree, so this session works inside it
            {validation?.dir ? ` (${validation.dir})` : ""} instead of creating a new one. Pick a
            folder in a different repo to unlock isolation.
          </div>
        )}
        {errors.map((e, i) => (
          <div key={i} className={[styles.banner, styles.bad].join(" ")} role="alert">
            ✕ {e.message}
          </div>
        ))}
        {startError && (
          <div className={[styles.banner, styles.bad].join(" ")} role="alert">
            ✕ Couldn&rsquo;t start session: {startError}
          </div>
        )}

        <div className={styles.actions}>
          <Button disabled={!canStart} onClick={() => void start(false)}>
            {submitting ? "Starting…" : "Start"} <kbd className={styles.kbd}>↵</kbd>
          </Button>
          <Button variant="primary" disabled={!canStart} onClick={() => void start(true)}>
            Start &amp; open <kbd className={styles.kbd}>{formatShortcut("Mod+↵")}</kbd>
          </Button>
        </div>
      </div>
    </PopoverPlacementContext.Provider>
  );
}

function ThisNode({ name }: { name: string }) {
  return (
    <span className={styles.node}>
      {name} <span className={styles.thisTag}>this</span>
    </span>
  );
}

function NewNode() {
  return <span className={[styles.node, styles.newNode].join(" ")}>new session</span>;
}
