import type { Config, ProfileSnapshot } from "../../router/types";
import { FIELD_LABELS, FIELD_SECTION, SECTION_IDS, type SectionId } from "./sections";
import { RESTART_REQUIRED_FIELDS } from "./restartRequired";
import { PROFILE_EXCLUDED_FIELDS } from "./profileSnapshot";

/** Semantic JSON equality — key order and reference identity don't matter. */
export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (a === null || b === null || typeof a !== "object") return false;
  const ak = Object.keys(a as Record<string, unknown>);
  const bk = Object.keys(b as Record<string, unknown>);
  if (ak.length !== bk.length) return false;
  return ak.every((k) =>
    deepEqual((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]),
  );
}

export interface DiffEntry {
  field: string;
  label: string;
  oldValue: unknown;
  newValue: unknown;
  restart: boolean;
}

/**
 * Fields the target profile's snapshot would change, relative to `current`
 * — mirrors pkg/contracts/profile.go's ProfileDrift direction (snapshot vs
 * live config), just computed client-side against the target instead of the
 * active profile. credential_bindings is diffed separately by name (see
 * computeCredentialDiff), not as a generic field here.
 */
export function computeProfileDiff(current: Config, targetSnapshot: ProfileSnapshot): DiffEntry[] {
  const entries: DiffEntry[] = [];
  const currentFields = current as unknown as Record<string, unknown>;
  for (const [field, newValue] of Object.entries(targetSnapshot)) {
    if (PROFILE_EXCLUDED_FIELDS.has(field) || field === "credential_bindings") continue;
    const oldValue = currentFields[field];
    if (deepEqual(oldValue, newValue)) continue;
    entries.push({
      field,
      label: FIELD_LABELS[field as keyof Config] ?? field,
      oldValue,
      newValue,
      restart: RESTART_REQUIRED_FIELDS.has(field as keyof Config),
    });
  }
  return entries;
}

export function groupBySection(entries: DiffEntry[]): Partial<Record<SectionId, DiffEntry[]>> {
  const grouped: Partial<Record<SectionId, DiffEntry[]>> = {};
  for (const entry of entries) {
    const section = FIELD_SECTION[entry.field as keyof Config];
    if (!section) continue;
    (grouped[section] ??= []).push(entry);
  }
  return grouped;
}

export function hasRestartChange(entries: DiffEntry[]): boolean {
  return entries.some((entry) => entry.restart);
}

/** SECTION_IDS filtered to those with at least one diff entry, in page order. */
export function orderedDiffSections(
  grouped: Partial<Record<SectionId, DiffEntry[]>>,
): SectionId[] {
  return SECTION_IDS.filter((id) => (grouped[id]?.length ?? 0) > 0);
}

/**
 * CredentialBindings semantics (pkg/contracts/config.go): empty/absent means
 * every stored credential is active — the backward-compatible default.
 */
export function isCredentialActive(name: string, bindings: string[] | undefined): boolean {
  if (!bindings || bindings.length === 0) return true;
  return bindings.includes(name);
}

export interface CredentialDiffEntry {
  name: string;
  wasActive: boolean;
  willBeActive: boolean;
}

/** Vault credentials whose active/inactive state changes under the target snapshot. */
export function computeCredentialDiff(
  credentialNames: string[],
  currentBindings: string[] | undefined,
  targetBindings: string[] | undefined,
): CredentialDiffEntry[] {
  const entries: CredentialDiffEntry[] = [];
  for (const name of credentialNames) {
    const wasActive = isCredentialActive(name, currentBindings);
    const willBeActive = isCredentialActive(name, targetBindings);
    if (wasActive !== willBeActive) entries.push({ name, wasActive, willBeActive });
  }
  return entries;
}
