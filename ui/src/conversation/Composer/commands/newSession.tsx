import { requestOpenCreatedSession, requestOpenSession } from "../../../appNavigation";
import type { CreateSessionPayload } from "../../../router/types";
import { NewSessionCard, type NewSessionBlock } from "./NewSessionCard";
import type { NewVariant } from "./newSessionLinks";
import type {
  CommandApi,
  CommandAvailability,
  CommandContext,
  CommandDescriptor,
} from "./types";

/** /new-leader's refusal, before anything is sent: one leader per session, and pipeline runs can't be led. */
export function leaderBlock(ctx: CommandContext): NewSessionBlock | undefined {
  const name = ctx.session?.name || "This session";
  if (ctx.pipelineRunning) {
    return {
      reason: "pipeline runs can't be led",
      message: `${name} is a pipeline run. Pipeline runs can be monitored, not led.`,
    };
  }
  if (ctx.leader) {
    return {
      reason: `${name} is already led by ${ctx.leader.name}`,
      message: `${name} already has a leader (${ctx.leader.name}). A session can have only one.`,
      leader: ctx.leader,
    };
  }
  return undefined;
}

/**
 * Starts the session. `open` (⌘↵) hands it to the Sessions surface the way
 * the Launchpad does — selected, provisioning shown, the goal auto-sent from
 * its composer. Otherwise (↵) the user stays here: the goal goes out from
 * here and a toast offers to open it. An empty goal (a /new-follower its
 * leader will brief) sends nothing.
 */
export async function startNewSession(
  api: CommandApi,
  payload: CreateSessionPayload,
  goal: string,
  open: boolean,
): Promise<void> {
  const { session } = await api.ctx.routerClient.createSession(payload);
  if (open) {
    requestOpenCreatedSession(session, goal);
    return;
  }
  // A follower created without a goal waits for its leader's first message.
  if (!goal) {
    api.notify(`Created ${session.name}`, {
      label: "Open",
      onClick: () => requestOpenSession(session.id),
    });
    return;
  }
  try {
    await api.ctx.routerClient.sendMessage(session.id, { text: goal, queue: true });
  } catch (err) {
    api.fail(
      `Started ${session.name}, but its goal didn't send: ${err instanceof Error ? err.message : String(err)}`,
    );
    return;
  }
  api.notify(`Started ${session.name}`, {
    label: "Open",
    onClick: () => requestOpenSession(session.id),
  });
}

const DESCRIPTIONS: Record<NewVariant, string> = {
  new: "Start a session",
  leader: "Start a session that leads this one",
  follower: "Start a session led by this one",
  monitor: "Start a session that watches this one",
};

/** The descriptor for one variant — new.ts, newLeader.ts, … each export one. */
export function newSessionCommand(variant: NewVariant): CommandDescriptor {
  const linked = variant !== "new";
  const available =
    variant === "leader"
      ? (ctx: CommandContext): CommandAvailability => {
          const block = leaderBlock(ctx);
          return block ? { ok: false, reason: block.reason } : { ok: true };
        }
      : undefined;
  return {
    name: variant === "new" ? "new" : `new-${variant}`,
    description: DESCRIPTIONS[variant],
    argHint: "[goal]",
    ...(linked ? { visible: (ctx: CommandContext) => !!ctx.linkedSessions } : {}),
    ...(available ? { available, runsWhenUnavailable: true } : {}),
    execute: ({ args }, api) => {
      const block = variant === "leader" ? leaderBlock(api.ctx) : undefined;
      api.openCard((close) => (
        <NewSessionCard
          variant={variant}
          session={api.ctx.session}
          sessionId={api.ctx.sessionId}
          routerClient={api.ctx.routerClient}
          initialGoal={args}
          block={block}
          onStart={async (payload, goal, open) => {
            // ↵ leaves at once and creates in the background, failures as a
            // toast; ⌘↵ keeps the card ("Starting…") until the session exists
            // to open, and a refusal stays on the card (it throws) to fix.
            if (open) {
              await startNewSession(api, payload, goal, true);
              close();
              return;
            }
            close();
            try {
              await startNewSession(api, payload, goal, false);
            } catch (err) {
              api.fail(`Couldn't start session: ${err instanceof Error ? err.message : String(err)}`);
            }
          }}
        />
      ));
    },
  };
}
