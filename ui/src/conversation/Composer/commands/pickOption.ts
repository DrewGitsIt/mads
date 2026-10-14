import type {
  CommandApi,
  CommandContext,
  CommandDescriptor,
  CommandInvocation,
  CommandOption,
} from "./types";

/**
 * The row a "/name arg" invocation means: the one picked from the submenu, or
 * — when the whole line was typed and Enter pressed — the option whose id or
 * label equals the typed argument (case-insensitive).
 */
export async function pickOption(
  command: CommandDescriptor,
  { args, option }: CommandInvocation,
  ctx: CommandContext,
): Promise<CommandOption | undefined> {
  if (option) return option;
  const wanted = args.trim().toLowerCase();
  if (!wanted || !command.options) return undefined;
  const options = await command.options(ctx);
  return options.find(
    (o) => o.id.toLowerCase() === wanted || o.label.toLowerCase() === wanted,
  );
}

/** /model's apply step: set the picked option and confirm with a toast. */
export async function applySessionOption(
  command: CommandDescriptor,
  invocation: CommandInvocation,
  api: CommandApi,
  field: "model",
  noun: string,
) {
  try {
    const option = await pickOption(command, invocation, api.ctx);
    if (!option) {
      api.fail(`No ${field} named "${invocation.args.trim()}".`);
      return;
    }
    const queued = await api.updateSession({ [field]: option.id });
    api.notify(
      `${noun} set to ${option.label}` +
        (queued ? " — applies when the current turn finishes." : ""),
    );
  } catch (err) {
    api.fail(err instanceof Error ? err.message : String(err));
  }
}
