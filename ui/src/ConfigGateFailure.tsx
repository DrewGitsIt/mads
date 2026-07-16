import { Button } from "./components/Button/Button";
import { Card } from "./components/Card/Card";
import { ROUTER_BASE } from "./router/client";
import styles from "./ConfigGateFailure.module.css";

export interface ConfigGateFailureProps {
  onRetry: () => void;
}

export function ConfigGateFailure({ onRetry }: ConfigGateFailureProps) {
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
