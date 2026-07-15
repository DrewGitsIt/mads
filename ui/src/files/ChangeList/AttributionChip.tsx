import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { AttributionKind } from "./attribution";

export interface AttributionChipProps {
  kind: AttributionKind;
  /** FileChange.actor, when kind is "actor" — falls back to "agent" when unset. */
  actor?: string;
}

const TONE: Record<AttributionKind, ChipTone> = {
  actor: "neutral",
  trailer: "info",
  session: "info",
  heuristic: "warn",
  orphan: "warn",
  unattributed: "draft",
};

const LABEL: Record<Exclude<AttributionKind, "actor">, string> = {
  trailer: "Trailer",
  session: "Session",
  heuristic: "Heuristic",
  orphan: "Orphan",
  unattributed: "Unattributed",
};

/**
 * Dumb presentational mapping from AttributionKind -> Chip tone/label
 * (HANDOFF §4). Reuses the kit's `draft` tone for "unattributed" — a
 * plain-provenance signal distinct from `Draft`/`DraftChip`
 * (src/components/Draft/Draft.tsx), whose "proposed content awaiting
 * acceptance" semantic doesn't fit an on-disk file change.
 */
export function AttributionChip({ kind, actor }: AttributionChipProps) {
  if (kind === "actor") {
    return <Chip tone={TONE.actor}>{actor ?? "agent"}</Chip>;
  }
  return <Chip tone={TONE[kind]}>{LABEL[kind]}</Chip>;
}
