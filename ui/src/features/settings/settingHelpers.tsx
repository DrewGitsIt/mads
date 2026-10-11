import type { SegmentedOption } from "../../components/SegmentedControl/SegmentedControl";
import type { Config } from "../../router/types";
import { RESTART_REQUIRED_FIELDS } from "./restartRequired";
import { SettingTag } from "./SettingRow";

/**
 * Option list that always contains `current`, even if it's not a known
 * choice — an unrecognized persisted value stays visible instead of being
 * silently rewritten.
 */
export function optionsWith(
  known: SegmentedOption[],
  current: string,
  label: (value: string) => string = (v) => v,
): SegmentedOption[] {
  return current && !known.some((o) => o.value === current)
    ? [{ value: current, label: label(current) }, ...known]
    : known;
}

/** The "Restart" marker for a field that only applies when mads restarts. */
export function restartTag(field: keyof Config) {
  return RESTART_REQUIRED_FIELDS.has(field) ? <SettingTag tone="warn">Restart</SettingTag> : undefined;
}
