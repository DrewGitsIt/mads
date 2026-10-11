import type { ConversationImportScan } from "../../router/types";
import { providerLabel } from "./providerBehaviors";

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function joinNames(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/**
 * "N conversations across M folders from Claude Code" — the one sentence both
 * import entry points (the first-run wizard step and Settings > General) use
 * to describe a scan, so they never disagree about what was found.
 */
export function summarizeConversationScan(
  scan: ConversationImportScan,
): string {
  return `${plural(scan.total, "conversation", "conversations")} across ${plural(scan.folders.length, "folder", "folders")} from ${joinNames(scan.providers.map((p) => providerLabel(p.provider)))}`;
}
