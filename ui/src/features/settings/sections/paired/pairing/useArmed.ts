import { useEffect, useState } from "react";

/** How long a confirming button stays disabled after it appears: no answering by a stray click or Enter. */
export const ARM_MS = 1000;

/**
 * Whether a button that just appeared (or whose question just changed, by
 * `key`) may be pressed yet: false for ARM_MS after `key` changes, and
 * while `key` is null (nothing to confirm).
 */
export function useArmed(key: string | null): boolean {
  const [armedFor, setArmedFor] = useState<string | null>(null);
  useEffect(() => {
    if (key === null) return;
    const t = setTimeout(() => setArmedFor(key), ARM_MS);
    return () => clearTimeout(t);
  }, [key]);
  return key !== null && armedFor === key;
}
