import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { UpdateCheckResult } from "../../../router/types";

export interface UseUpdateCheck {
  currentVersion: string;
  result: UpdateCheckResult | null;
  checking: boolean;
  check: () => void;
}

/**
 * GET /version is safe to fetch on mount (it's local, no network call out).
 * POST /update/check is not: it's the one call that reaches the release
 * feed, so it only ever fires from check(), in response to the user
 * clicking "Check for updates" — never on mount, never on a timer (§14.3's
 * "no auto-phone-home" guarantee).
 */
export function useUpdateCheck(): UseUpdateCheck {
  const [currentVersion, setCurrentVersion] = useState("");
  const [result, setResult] = useState<UpdateCheckResult | null>(null);
  const [checking, setChecking] = useState(false);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    router.getVersion().then((info) => {
      if (mounted.current) setCurrentVersion(info.version);
    });
    return () => {
      mounted.current = false;
    };
  }, []);

  const check = useCallback(() => {
    setChecking(true);
    router.checkForUpdate().then((r) => {
      if (mounted.current) {
        setResult(r);
        setChecking(false);
      }
    });
  }, []);

  return { currentVersion, result, checking, check };
}
