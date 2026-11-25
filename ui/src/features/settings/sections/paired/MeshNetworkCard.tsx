import { useState } from "react";
import { usePoll } from "./usePoll";
import { Button } from "../../../../components/Button/Button";
import { Dialog, dialogStyles, DialogText } from "../../../../components/Dialog/Dialog";
import { Disclosure } from "../../../../components/Disclosure/Disclosure";
import { Switch } from "../../../../components/Switch/Switch";
import { InfoTip, Tooltip } from "../../../../components/Tooltip/Tooltip";
import { SettingRow, SettingTag } from "../../SettingRow";
import { SettingsCard } from "../../SettingsCard";
import { devicesApi, DevicesError, selfStore, type SelfDevice } from "../../../../router/devices";
import { cardState, safeSignInURL, settling, chosenNetwork, inDays, knockLine, meaningLine, networkIcon, TAILSCALE_ADMIN, TAILSCALE_ID, tailscaleBanner, wordFor } from "./network";
import { NetIcon } from "./NetIcon";
import paired from "./PairedDevices.module.css";
import styles from "./Mesh.module.css";

/** How often the card re-reads its state while it isn't Reachable, just after a network change, and once it is. */
export const STATE_POLL_MS = 5_000;
export const SETTLING_POLL_MS = 1_000;
export const REACHABLE_POLL_MS = 30_000;

export interface MeshNetworkCardProps {
  self: SelfDevice;
  host: string;
  onChange: () => void;
  onToast: (t: { tone: "ok" | "info" | "bad"; title: string; description?: string }) => void;
  now?: Date;
}

/**
 * Mesh network (U2): this device's one network, what it means and who can
 * knock, whether it's reachable there now, Change…, the Tailscale
 * prevention notes (key expiry, sign in again), and Advanced options (the
 * Tailscale identity check, the internet, the port).
 */
/** A sign-in link refused, here or by the Router: never opened. */
const UNEXPECTED_LINK = { tone: "bad", title: "Tailscale gave an unexpected sign-in link", description: "Sign in from the Tailscale app instead." } as const;

export function MeshNetworkCard({ self, host, onChange, onToast, now: fixedNow }: MeshNetworkCardProps) {
  const now = fixedNow ?? new Date();
  const mesh = self.mesh!;
  const n = chosenNetwork(mesh);
  const state = cardState(self, host, settling(now.getTime()));
  const banner = tailscaleBanner(mesh.tailscale, n, now);
  const admin = TAILSCALE_ADMIN;
  const [confirmPublic, setConfirmPublic] = useState(false);
  const [saving, setSaving] = useState(false);
  const [signingIn, setSigningIn] = useState(false);
  const [error, setError] = useState<string>();
  const word = wordFor(n);

  // Whether the Mesh Service answers is checked live by the Router, and
  // nothing on the bus says when it changes: re-read now and then, often
  // while it isn't Reachable, slowly once it is, never while the page is
  // hidden (T4, S3).
  const settled = state.dot === "ok";
  // Just after a network change, every second: the card turns Reachable as
  // soon as the Mesh Service listens, as Add devices says (Q3).
  const getting = state.dot === "muted";
  const reread = () =>
    selfStore.refresh().then(() => {
      const err = selfStore.get().error;
      if (err) throw new Error(err);
    });
  usePoll(
    reread,
    true,
    settled
      ? { every: REACHABLE_POLL_MS, max: 120_000 }
      : getting
        ? { every: SETTLING_POLL_MS, max: 5_000 }
        : { every: STATE_POLL_MS, max: 60_000, slowAfter: 120_000 },
  );

  const save = async (s: { tailnet_check?: boolean; public?: boolean; confirm_public?: boolean }) => {
    setSaving(true);
    setError(undefined);
    try {
      selfStore.set(await devicesApi.setMesh(s));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  // Only when tailscaled says it needs a sign-in: starting one while
  // signed in would sign this device out.
  const signIn = async () => {
    setSigningIn(true);
    try {
      const r = await devicesApi.tailscaleLogin();
      const url = safeSignInURL(r.auth_url, r.control_url ?? mesh.tailscale?.control_url);
      if (url) window.open(url, "_blank", "noreferrer");
      else if (r.auth_url) {
        onToast(UNEXPECTED_LINK);
      } else onToast({ tone: "info", title: "Tailscale sign-in started", description: "Finish it in the Tailscale app." });
    } catch (err) {
      if (err instanceof DevicesError && err.code === "unexpected_auth_url") {
        onToast(UNEXPECTED_LINK);
      } else if (err instanceof DevicesError && err.code === "not_needed") {
        onToast({ tone: "ok", title: "Tailscale is signed in", description: `Your devices can reach this ${host} again.` });
      } else {
        onToast({ tone: "bad", title: "Couldn't start the Tailscale sign-in", description: err instanceof Error ? err.message : String(err) });
      }
    } finally {
      setSigningIn(false);
      void selfStore.refresh();
    }
  };

  const knock = n ? knockLine(n) : undefined;
  return (
    <SettingsCard title="Mesh network">
      <div className={styles.cardMain} data-mesh-network={n?.id ?? ""}>
        <NetIcon icon={n ? networkIcon(n) : "wifi"} />
        <div>
          <div className={styles.cardValue}>{word}</div>
          {n && (
            <div className={styles.cardMeaning}>
              {meaningLine(n).replace(/^[^·]*· /, "")} {knock?.text}
            </div>
          )}
        </div>
        <Tooltip tip={state.tip} align="end">
          <span className={styles.cardState} data-mesh-state={state.text}>
            <span className={paired.dot} data-dot={state.dot} aria-hidden="true" />
            {state.text}
          </span>
        </Tooltip>
        <Button size="sm" onClick={onChange}>
          Change…
        </Button>
      </div>
      {banner && (
        <div className={styles.cardBanner}>
          {banner.kind === "signed_out" ? (
            <div className={styles.banner} data-tone="bad" role="alert" data-tailscale="signed_out">
              <span className={styles.bannerIcon} aria-hidden="true">
                !
              </span>
              <div className={styles.bannerMessage}>
                <span className={styles.bannerTitle}>Tailscale needs you to sign in again</span>
                <span className={styles.bannerDescription}>Until you do, your other devices can't reach this {host}.</span>
              </div>
              <Button variant="primary" size="sm" disabled={signingIn} onClick={() => void signIn()}>
                Sign in to Tailscale
              </Button>
            </div>
          ) : (
            <div className={styles.banner} data-tone={banner.kind === "expiring" ? "warn" : "info"} role="status" data-tailscale={banner.kind}>
              <span className={styles.bannerIcon} aria-hidden="true">
                {banner.kind === "expiring" ? "!" : "i"}
              </span>
              <div className={styles.bannerMessage}>
                <span className={styles.bannerTitle}>
                  {banner.kind === "expiring" ? `Tailscale signs this ${host} out ${inDays(banner.days)}` : `Tailscale signs this ${host} out on ${banner.on}`}
                </span>
                <span className={styles.bannerDescription}>For a machine you reach remotely, turn off key expiry so it stays reachable.</span>
              </div>
              <a className={styles.linkBtn} href={admin} target="_blank" rel="noreferrer">
                Open Tailscale admin
              </a>
            </div>
          )}
        </div>
      )}
      {state.problem && banner?.kind !== "signed_out" && (
        <div className={styles.cardBanner}>
          <div className={styles.banner} data-tone="warn" role="status" data-mesh-problem="">
            <span className={styles.bannerIcon} aria-hidden="true">
              !
            </span>
            <div className={styles.bannerMessage}>
              <span className={styles.bannerTitle}>{state.problem.title}</span>
              <span className={styles.bannerDescription}>{state.problem.description}</span>
            </div>
            <span />
          </div>
        </div>
      )}
      <Disclosure summary="Advanced options" className={styles.cardAdvanced}>
        <SettingsCard>
          {n?.id === TAILSCALE_ID && (
            <SettingRow
              label="Check Tailscale identity"
              htmlFor="ts-check"
              tag={
                <InfoTip label="Check Tailscale identity">
                  Asks Tailscale who each connection is from. It can turn a device away. It never lets one in, and never changes what a
                  device may do here.
                </InfoTip>
              }
              help={
                mesh.tailnet_check_locked
                  ? "Set by -require-tailnet on this device's Mesh Service, which overrides this switch until it's started without it."
                  : mesh.tailnet_check && mesh.tailnet_check_status === "unavailable"
                    ? "Tailscale can't be asked right now, so the Tailscale network turns everyone away until it can."
                    : "Only accept devices signed in to your tailnet as you."
              }
            >
              <Switch
                id="ts-check"
                checked={mesh.tailnet_check}
                disabled={saving || Boolean(mesh.tailnet_check_locked)}
                onChange={(v) => void save({ tailnet_check: v })}
              />
            </SettingRow>
          )}
          <SettingRow
            label="Also reachable from the internet"
            htmlFor="mesh-public"
            tag={mesh.public ? <SettingTag tone="warn">Less safe</SettingTag> : undefined}
            help={
              mesh.public
                ? "Anyone on the internet can try to connect. Only devices you add get in."
                : "Off. Most people never need this: a VPN reaches your devices from anywhere."
            }
          >
            <Switch id="mesh-public" checked={mesh.public} disabled={saving} onChange={(v) => (v ? setConfirmPublic(true) : void save({ public: false }))} />
          </SettingRow>
          <SettingRow label="Port" help="The same on every network.">
            <code className={styles.port}>{mesh.port}</code>
          </SettingRow>
        </SettingsCard>
        {error && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
      </Disclosure>
      {confirmPublic && (
        <Dialog
          alert
          confirm
          title="Also reachable from the internet?"
          onDismiss={() => setConfirmPublic(false)}
          actions={
            <>
              <Button variant="ghost" onClick={() => setConfirmPublic(false)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                className={dialogStyles.danger}
                onClick={() => {
                  setConfirmPublic(false);
                  void save({ public: true, confirm_public: true });
                }}
              >
                Make it reachable
              </Button>
            </>
          }
        >
          <DialogText>
            <p>
              Anyone on the internet can try to connect to mads on this {host}. Only devices you add get in, but scanners find open ports
              within hours, and anyone who connects learns that mads runs here and this device's fingerprint. This listener has no
              defences tuned for scanners yet, only the same connection limits as a home network.
            </p>
            <p>Most people never need this. A VPN such as Tailscale reaches your devices from anywhere without it.</p>
          </DialogText>
        </Dialog>
      )}
    </SettingsCard>
  );
}
