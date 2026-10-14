import type { ReactNode } from "react";
import type { RouterClient } from "../../../router/client";
import type { CompactionSupport } from "../../../router/compaction";
import type { Session } from "../../../router/types";

/**
 * What a command can see when deciding whether it's available and what its
 * submenu lists. The Composer builds one per render from its props; a command
 * that needs more (the Session, GET /providers capabilities, links, feature
 * flags) adds an optional field here and the Composer prop that fills it.
 */
export interface CommandContext {
  routerClient: RouterClient;
  sessionId: string;
  agentId?: string;
  /** The session's current provider — "/model" lists its models. */
  provider?: string;
  /** What the session's harness can do about compaction — "/compact" gates on it. */
  compaction?: CompactionSupport;
  /** This session's record — the /new card prefills its pills from it. */
  session?: Session;
  /** feature_flags.linked_sessions — /new-leader, /new-follower and /new-monitor are hidden without it. */
  linkedSessions?: boolean;
  /** The session leading this one (its orchestrate holder), if any — /new-leader is unavailable then. */
  leader?: { id: string; name: string };
  /** A pipeline run is live in this session — it can be monitored, not led. */
  pipelineRunning?: boolean;
}

/** available()'s answer. A reason is shown inline on the disabled menu row and when typed + Enter. */
export type CommandAvailability = { ok: true } | { ok: false; reason: string };

/** One row of a command's second-level menu ("/model ›"). */
export interface CommandOption {
  /** What execute receives — an agent id, a model id. */
  id: string;
  label: string;
  detail?: string;
}

/** A card body; call `close` to dismiss it (focus returns to the composer). */
export type CommandCardRender = (close: () => void) => ReactNode;

/** A toast's link/button, e.g. "Open task". */
export interface CommandToastAction {
  label: string;
  onClick: () => void;
}

/**
 * What execute can do. The Composer has already cleared the command's text
 * from the input by the time execute runs; nothing here writes to the
 * transcript.
 */
export interface CommandApi {
  ctx: CommandContext;
  /** Applies a model change — the dock model picker's commit path. Resolves true when queued behind a running turn. */
  updateSession: (
    patch: Partial<Pick<Session, "model">>,
  ) => Promise<boolean | void>;
  /** A success toast, optionally with an action link. */
  notify: (message: string, action?: CommandToastAction) => void;
  /** A failure toast — for errors that come back from the Router. */
  fail: (message: string) => void;
  /** Opens a card above the composer, replacing any open one. */
  openCard: (render: CommandCardRender) => void;
  /** Opens a task of this session in the Tasks tab. Absent when the host can't navigate. */
  openTask?: (taskId: string) => void;
  /** Compacts the session through the same path as the "Compact now" button. Absent when the host can't. */
  compact?: (instructions?: string) => void;
}

/** One invocation: the text after the command name, and the submenu row picked, if any. */
export interface CommandInvocation {
  args: string;
  option?: CommandOption;
}

/**
 * A "/" command. Add one by writing a descriptor in its own file under
 * commands/ and listing it in registry.ts's COMMANDS.
 */
export interface CommandDescriptor {
  /** Without the slash: "task". */
  name: string;
  /** Other names that resolve to this command and match in the menu ("status" → usage). */
  aliases?: string[];
  description: string;
  /** Shown after the name in the menu, e.g. "<title>", "[focus]". Its presence means picking the row inserts "/name " for typing args instead of executing. */
  argHint?: string | ((ctx: CommandContext) => string | undefined);
  /** When set, invoking with no args shows this inline and runs nothing ("/task needs a title"). */
  argRequired?: string;
  /** Checked when args are typed: a message blocks the run inline and keeps the text ("/compact <text>" on a harness without focus). */
  argError?: (args: string, ctx: CommandContext) => string | undefined;
  /** Second-level menu: picking "/name" descends to these rows, filtered by what's typed after the space. */
  options?: (ctx: CommandContext) => Promise<CommandOption[]>;
  /** Hidden entirely when false — not in the menu and not recognized when typed (e.g. a feature flag is off). */
  visible?: (ctx: CommandContext) => boolean;
  /** Absent → always available. */
  available?: (ctx: CommandContext) => CommandAvailability;
  /**
   * Unavailable still disables the menu row, but typing the command and
   * pressing Enter runs execute anyway — for a command that shows its own
   * blocked state (the /new-leader card's "already has a leader" banner)
   * rather than a bare notice.
   */
  runsWhenUnavailable?: boolean;
  execute: (
    invocation: CommandInvocation,
    api: CommandApi,
  ) => void | Promise<void>;
}
