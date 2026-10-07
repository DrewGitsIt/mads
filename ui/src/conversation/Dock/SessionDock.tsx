import type { ReactNode } from "react";
import type { AgentIndex } from "../../router/agentRef";
import type { Session, SessionTrust, Usage } from "../../router/types";
import type { LandCardModel } from "../Worktree/worktreeCards";
import { ContextRim } from "./ContextRim";
import { StatusLine } from "./StatusLine";
import styles from "./SessionDock.module.css";

/** Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D4) — same shape ConversationHeader's `work` had. */
export interface DockWork {
  /** Opens the commit flow (one-click Commit or the Commit dialog — the caller decides). */
  onCommit: () => void;
  /** Runs Land. Fire and forget: the outcome comes back through `landOutcome`. */
  onLand: () => void;
  /** Puts an editable message in the composer (tidy up / resolve / check). Never sends on its own. */
  onAskAgent?: (text: string) => void;
  /** A Land is in flight (useWorktreeCards().landing). */
  landing?: boolean;
  /** A one-click Commit is in flight (useWorktreeCards().committing). */
  committing?: boolean;
}

/**
 * Props of the session dock — the edge-to-edge band at the bottom of the
 * conversation that replaces ConversationHeader (docs/SESSION-DOCK-TASKS.md,
 * mockup docs/mockups/composer-options.html §E / §E·Terminal T2).
 *
 * Layout, top to bottom: the context rim (2px hairline), `children`, the
 * status line. `sheet` floats above the dock (bottom: 100%, full width) and
 * never changes the dock's own height.
 */
export interface SessionDockProps {
  session: Session;
  /** Latest usage (turn_end frame, else session.last_usage). Absent → the rim is a plain hairline, no label. */
  usage?: Usage;
  /** Fallback context window for the rim (contextWindowForModel); usage.context_window wins. */
  contextWindow?: number;
  /** Chat only: the rim label's "Compact" link from 80%. Ignored in terminal mode. */
  onCompact?: () => void;

  /** Active view. Terminal adds the leading "💬 Chat" and the "⌘E compose" segments. */
  mode: "chat" | "terminal";
  /**
   * Switches view. Only the terminal-mode "💬 Chat" segment calls it (with
   * "chat"); in chat mode the view switch lives in the composer's + menu.
   */
  onModeChange: (mode: "chat" | "terminal") => void;
  /** Terminal only: the "⌘E compose" segment toggles the compose sheet. Absent → no segment. */
  onComposeToggle?: () => void;
  /** Terminal only: the compose sheet is open (the compose segment shows pressed). */
  composeOpen?: boolean;

  /** Personas by id and name (useAgentIndex), so the persona segment never prints an `agt_…` id. Undefined while loading. */
  agents?: AgentIndex;
  /** The session's resolved workspace *name* (useSessionWorkspace); unresolved → the folder's basename shows. */
  workspaceName?: string;
  /** The checkout's open branch for a session working in place (useCheckoutBranch). */
  checkoutBranch?: string;

  /** Persona edits (free text, committed trimmed and only when changed). */
  onUpdateSession?: (patch: Partial<Pick<Session, "name" | "agent">>) => void;
  /** Live model switch (POST /sessions/{id}/model). Without discovered models the segment is static. */
  onChangeModel?: (model: string) => void;
  /** Live trust switch (POST /sessions/{id}/trust). Absent → trust is a static segment. */
  onChangeTrust?: (trust: SessionTrust) => void;

  /** Commit/Land control and the ⎇ popover's buttons. Absent → no control (the ⎇ segment still shows). */
  work?: DockWork;
  /**
   * The newest Land outcome — latestLandOutcome(useWorktreeCards().cards).
   * After a Land click the ⎇ popover shows the first outcome that differs
   * from the one current at the click (a repeated identical refusal counts
   * as different: its `repeats` grows).
   */
  landOutcome?: LandCardModel;

  /** The input region (chat: chips + composer row / expanded editor). Terminal: usually nothing. */
  children?: ReactNode;
  /** Floats above the dock over the xterm (terminal compose). Doesn't resize the dock or the PTY. */
  sheet?: ReactNode;
}

/**
 * The session dock: rim, input slot, status line — everything about the
 * session at the bottom edge, in both views. It is the narrow-ladder size
 * container: below ~700px persona/model/branch shorten and "Trust: " drops,
 * below ~520px persona/model go icon-only and the branch hides. The trust
 * value and the Commit/Land control never collapse.
 */
export function SessionDock(props: SessionDockProps) {
  const { usage, contextWindow, onCompact, mode, children, sheet } = props;
  return (
    <div className={styles.dock} data-testid="session-dock" data-mode={mode}>
      {sheet != null && sheet !== false && (
        <div className={styles.sheet} data-testid="dock-sheet">
          {sheet}
        </div>
      )}
      <ContextRim usage={usage} contextWindow={contextWindow} onCompact={mode === "chat" ? onCompact : undefined} />
      {children}
      <StatusLine {...props} />
    </div>
  );
}
