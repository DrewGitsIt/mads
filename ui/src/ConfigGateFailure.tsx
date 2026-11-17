import { useEffect, useState } from "react";
import { Button } from "./components/Button/Button";
import { Card } from "./components/Card/Card";
import { ROUTER_BASE } from "./router/client";
import styles from "./ConfigGateFailure.module.css";

export interface ConfigGateFailureProps {
  onRetry: () => void;
  /**
   * The Router answered 401: the window's requests carried no valid install
   * token. The app's main process adds it below the page
   * (docs/LOCAL-FRONT-DOOR-DESIGN.md H7), so this means main couldn't read
   * the token file, or the token changed under a Router that hasn't caught up.
   */
  unauthorized?: boolean;
}

/** Where main reads the token, and why it couldn't (Electron only). */
function useTokenPath(enabled: boolean): { path: string; problem?: string } | undefined {
  const [info, setInfo] = useState<{ path: string; problem?: string }>();
  useEffect(() => {
    const auth = typeof window !== "undefined" ? window.mads?.auth : undefined;
    if (!enabled || !auth) return;
    let live = true;
    auth
      .tokenPath()
      .then((i) => live && setInfo(i))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [enabled]);
  return info;
}

export function ConfigGateFailure({ onRetry, unauthorized }: ConfigGateFailureProps) {
  const token = useTokenPath(!!unauthorized);
  if (unauthorized) {
    return (
      <div className={styles.wrap}>
        <Card className={styles.card}>
          <div className={styles.title}>mads can't reach its token</div>
          <div className={styles.body}>
            The Router refused this window: mads couldn't use its install token at{" "}
            <code>{token?.path ?? "$MADS_HOME/local.token"}</code>
            {token?.problem ? ` (${token.problem})` : ""}. Restart the app; if that
            doesn't help, check that file.
          </div>
          <Button variant="primary" onClick={onRetry}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }
  return (
    <div className={styles.wrap}>
      <Card className={styles.card}>
        <div className={styles.title}>Can't reach the mads backend</div>
        <div className={styles.body}>
          No response from the Router at <code>{ROUTER_BASE}</code>. Make sure
          it's running, then retry.
        </div>
        <Button variant="primary" onClick={onRetry}>
          Retry
        </Button>
      </Card>
    </div>
  );
}
