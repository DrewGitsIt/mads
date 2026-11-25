import { useEffect, useRef, useState } from "react";
import { Button } from "../../../../../components/Button/Button";
import { Dialog, DialogText } from "../../../../../components/Dialog/Dialog";
import { devicesApi, DevicesError, pairRequestsStore, type PairingCode } from "../../../../../router/devices";
import styles from "./Pairing.module.css";

export const mmss = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;

export interface CopyCodeProps {
  host: string;
  onClose: () => void;
  /** No mesh network yet: Copy starts Set up mesh instead. */
  onSetUp?: () => void;
  /** A mesh network is chosen: a 409 no_mesh_network means its Mesh Service isn't listening yet, so it waits (T5). */
  configured?: boolean;
  /** The other way round: paste the other device's code here. */
  onPaste?: () => void;
}

/** How often, and how many times, a code is asked for again while this device gets ready (its Mesh Service starting to listen). */
export const READY_RETRY_MS = 2_000;
export const READY_RETRIES = 15;

/**
 * Copy this device's pairing code: made on open, single use, five minutes,
 * and copied to the clipboard as soon as it's made (P3).
 * It names the networks it tells the other device about, never the
 * addresses. When a device presents it, the approval prompt takes over
 * and this closes; closing retires an unused code. With no mesh network
 * yet (409 no_mesh_network), Set up mesh comes first.
 */
export function CopyCode({ host, onClose, onSetUp, configured, onPaste }: CopyCodeProps) {
  const [code, setCode] = useState<PairingCode | null>(null);
  const [error, setError] = useState<string>();
  const [now, setNow] = useState(() => Date.now());
  const [copied, setCopied] = useState(false);
  const madeAt = useRef(0);
  const setUp = useRef(onSetUp);
  setUp.current = onSetUp;
  const used = useRef(false);
  const [gettingReady, setGettingReady] = useState(false);
  const retry = useRef<{ n: number; t?: ReturnType<typeof setTimeout>; live: boolean }>({ n: 0, live: true });
  const isConfigured = useRef(configured);
  isConfigured.current = configured;

  const make = () => {
    setError(undefined);
    setCode(null);
    setCopied(false);
    madeAt.current = Date.now();
    devicesApi.createCode().then(
      (c) => {
        if (!retry.current.live) return;
        setGettingReady(false);
        setCode(c);
        // Already copied: the click that opened this is the user's ask. A
        // clipboard that refuses (no focus, no permission) leaves Copy.
        navigator.clipboard?.writeText(c.code).then(
          () => retry.current.live && setCopied(true),
          () => {},
        );
      },
      (err: unknown) => {
        if (!retry.current.live) return;
        if (err instanceof DevicesError && err.code === "no_mesh_network") {
          // A network is chosen, and its Mesh Service isn't listening yet
          // (just after setup): wait for it, never back to choosing one.
          if (isConfigured.current) {
            if (retry.current.n < READY_RETRIES) {
              retry.current.n++;
              setGettingReady(true);
              retry.current.t = setTimeout(make, READY_RETRY_MS);
              return;
            }
            setGettingReady(false);
            setError(`This ${host} isn't listening on its mesh network yet. Check that mads's Mesh Service is running, then try again.`);
            return;
          }
          if (setUp.current) {
            setUp.current();
            return;
          }
        }
        setGettingReady(false);
        setError(err instanceof Error ? err.message : String(err));
      },
    );
  };
  useEffect(() => {
    const r = retry.current;
    r.live = true;
    make();
    return () => {
      r.live = false;
      clearTimeout(r.t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- made once, on open
  }, []);

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

  const nets = code?.networks?.map((n) => n.label).filter(Boolean).join(" and ");
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
          Close
        </Button>
      }
    >
      <DialogText>
        On the other device, open Settings → Paired devices and paste it there. It tells that device how to find this {host}
        {nets ? ` (on ${nets})` : ""} and proves it's this one.
      </DialogText>
      {error ? (
        <p className={styles.error} role="alert">
          {error}
        </p>
      ) : (
        <>
          <div className={styles.codeString} aria-label="Pairing code" data-pairing-code="">
            {code?.code ?? (gettingReady ? "Getting this device ready…" : "Making a code…")}
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
              {copied ? "Copy again" : "Copy"}
            </Button>
            <span className={styles.timer} data-copied={copied || undefined}>
              {copied ? "Copied. " : ""}Works once · expires in {mmss(left)}
            </span>
          </div>
        </>
      )}
      <p className={styles.help}>
        Closing this cancels the code if it hasn't been used.
        {onPaste && (
          <>
            {" "}
            Have the other device's code instead?{" "}
            <button type="button" className={styles.suggestLink} onClick={onPaste}>
              Paste it here
            </button>
            .
          </>
        )}
      </p>
    </Dialog>
  );
}
