import { useMemo } from "react";
import { useProviderInfos } from "./useProviderInfos";
import type { ProviderInfo, ProviderSupport } from "./types";

/**
 * What a provider's harness can do about compaction, from its capability
 * matrix (GET /providers; providers/provider/matrix.go `compaction` and
 * `compaction_focus`). Compaction is never emulated: where `compacts` is
 * false every Compact affordance hides, and /compact is unavailable.
 */
export interface CompactionSupport {
  /** The harness runs its own compaction pass on request. */
  compacts: boolean;
  /** It also takes focus text (`/compact <instructions>`). */
  takesFocus: boolean;
}

const works = (s: ProviderSupport | undefined) => s === "native" || s === "emulated";

/**
 * A provider's compaction support. An unknown provider or matrix (still
 * loading, a failed fetch, a Router predating these rows) reads as
 * supported — useProviderInfos' "unknown is no annotation" rule: only an
 * explicit declaration hides Compact, and a wrong guess still meets the
 * Router's own 400.
 */
export function compactionSupport(
  infos: ProviderInfo[],
  provider: string | undefined,
): CompactionSupport {
  const matrix = infos.find((p) => p.name === provider)?.matrix;
  const compacts = matrix?.compaction === undefined ? true : works(matrix.compaction);
  const takesFocus = compacts && (matrix?.compaction_focus === undefined ? true : works(matrix.compaction_focus));
  return { compacts, takesFocus };
}

/**
 * compactionSupport for one session's provider, live from GET /providers.
 * Stable identity while the answer is unchanged — consumers put it in
 * useMemo/effect deps (the Composer's command context).
 */
export function useCompactionSupport(provider: string | undefined): CompactionSupport {
  const { compacts, takesFocus } = compactionSupport(useProviderInfos(), provider);
  return useMemo(() => ({ compacts, takesFocus }), [compacts, takesFocus]);
}
