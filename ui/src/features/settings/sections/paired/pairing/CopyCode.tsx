import { useEffect, useRef, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../../components/Dialog/Dialog";
import { devicesApi, pairRequestsStore, type PairingCode } from "../../../../../router/devices";
import styles from "./Pairing.module.css";

export const mmss = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;

export interface CopyCodeProps {
  host: string;
  onClose: () => void;
}

/**
 * Copy this device's pairing code: made on open, single use, five minutes.
 * When a device presents it, the approval prompt takes over and this
 * closes; Stop retires the code.
 */
export function CopyCode({ host, onClose }: CopyCodeProps) {
  const [code, setCode] = useState<PairingCode | null>(null);
  const [error, setError] = useState<string>();
  const [now, setNow] = useState(() => Date.now());
  const [copied, setCopied] = useState(false);
  const madeAt = useRef(0);
  const used = useRef(false);

  const make = () => {
    setError(undefined);
    setCode(null);
    setCopied(false);
    madeAt.current = Date.now();
    devicesApi.createCode().then(setCode, (err: unknown) => setError(err instanceof Error ? err.message : String(err)));
  };
  useEffect(make, []);

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  // A request made with this code: the prompt (PairRequestWatcher) shows; this goes.
  useEffect(() => {
    return pairRequestsStore.subscribe(() => {
      const reqs = pairRequestsStore.get().value ?? [];
      if (reqs.some((r) => r.state === "pending" && Date.parse(r.created_at) >= madeAt.current - 1000)) {
        used.current = true;
        onClose();
      }
    });
  }, [onClose]);

  const stop = () => {
    if (code && !used.current) void devicesApi.deleteCode(code.id).catch(() => {});
    onClose();
  };

  const left = code ? Math.max(0, Math.round((Date.parse(code.expires_at) - now) / 1000)) : 300;
  if (code && left <= 0) {
    return (
      <Dialog
        title="Pairing code expired"
        onDismiss={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button variant="primary" onClick={make}>
              Make a new code
            </Button>
          </>
        }
      >
        <div className={styles.status}>
          <span className={styles.failMark} aria-hidden="true">
            !
          </span>
          <DialogText>Codes last 5 minutes and work once. Nothing was paired.</DialogText>
        </div>
      </Dialog>
    );
  }
  return (
    <Dialog
      title="Copy this device's pairing code"
      onDismiss={stop}
      actions={
        <Button variant="secondary" onClick={stop}>
          Stop
        </Button>
      }
    >
      <DialogText>
        Paste it on the other device: Pair a device → Paste a pairing code. It holds this {host}'s name, fingerprint and a
        one-time secret.
      </DialogText>
      {error ? (
        <p className={styles.error} role="alert">
          {error}
        </p>
      ) : (
        <>
          <div className={styles.codeString} aria-label="Pairing code" data-pairing-code="">
            {code?.code ?? "Making a code…"}
          </div>
          <div className={styles.codeRow}>
            <Button
              variant="primary"
              data-autofocus=""
              disabled={!code}
              onClick={() => {
                if (!code) return;
                void navigator.clipboard?.writeText(code.code).catch(() => {});
                setCopied(true);
              }}
            >
              {copied ? "Copied" : "Copy"}
            </Button>
            <span className={styles.timer}>Works once · expires in {mmss(left)}</span>
          </div>
        </>
      )}
      <p className={styles.help}>When it's pasted, you approve the device here and choose what it may do.</p>
    </Dialog>
  );
}
