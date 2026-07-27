import type {
  AgentPermissions,
  IntentMatrix,
  PermissionIntent,
  ProviderSupport,
} from "../router/types";

/** The full v1 vocabulary, in display order — mirrors pkg/contracts/permission.go PermissionIntents. */
export const PERMISSION_INTENTS: PermissionIntent[] = [
  "deny_commands",
  "path_scope",
  "read_only",
  "require_approval",
];

export const PERMISSION_INTENT_LABELS: Record<PermissionIntent, string> = {
  deny_commands: "Block commands",
  path_scope: "Restrict file paths",
  read_only: "Read-only mode",
  require_approval: "Require approval",
};

export const PERMISSION_INTENT_DESCRIPTIONS: Record<PermissionIntent, string> = {
  deny_commands: "Bash commands and other shell/tool invocations are blocked.",
  path_scope: "File reads and writes are limited to the listed paths.",
  read_only: "File edits and command execution are both blocked.",
  require_approval: "Tool use pauses for a human approval prompt before running.",
};

/**
 * Mirrors pkg/contracts/permission.go AgentPermissions.Intents(): a non-empty
 * denied_commands list is deny_commands, a non-empty allowed_paths list is
 * path_scope. read_only/require_approval have no AgentPermissions field yet
 * (session-trust-only — see SessionTrust.Intents() in that file), so they
 * never appear here.
 */
export function agentIntents(perms?: AgentPermissions): PermissionIntent[] {
  const out: PermissionIntent[] = [];
  if (perms?.denied_commands?.length) out.push("deny_commands");
  if (perms?.allowed_paths?.length) out.push("path_scope");
  return out;
}

/**
 * No matrix (provider not yet selected, or the provider doesn't implement
 * IntentEnforcer) is treated the same as "unsupported" — never claim an
 * intent is enforced before the provider has said so.
 */
export function intentSupport(
  matrix: IntentMatrix | undefined,
  intent: PermissionIntent,
): ProviderSupport {
  return matrix?.[intent] ?? "unsupported";
}

export function intentEnforcementNote(support: ProviderSupport): string {
  switch (support) {
    case "native":
      return "enforced by the selected provider.";
    case "emulated":
      return "best-effort with the selected provider — not a hard guarantee.";
    case "unsupported":
      return "not enforced by the selected provider.";
  }
}

export interface AgentIntentNote {
  intent: PermissionIntent;
  label: string;
  support: ProviderSupport;
  note: string;
}

/** One row per intent the agent record actually declares, honest about whether the selected provider enforces it. */
export function agentIntentNotes(
  perms: AgentPermissions | undefined,
  matrix: IntentMatrix | undefined,
): AgentIntentNote[] {
  return agentIntents(perms).map((intent) => {
    const support = intentSupport(matrix, intent);
    return {
      intent,
      label: PERMISSION_INTENT_LABELS[intent],
      support,
      note: intentEnforcementNote(support),
    };
  });
}
