import { useState } from "react";
import { Caret } from "../../../components/Caret/Caret";
import { Popover } from "../../../components/Popover/Popover";
import { useMesh } from "../../../router/mesh";
import type { PeerStatus, SessionHost } from "../../../router/types";
import pillStyles from "./pills.module.css";

/** The devices a session can be started on from here: connected, and granted "control". */
export function launchablePeers(peers: PeerStatus[]): PeerStatus[] {
  return peers.filter((p) => p.reachability === "connected" && (p.access ?? []).includes("control"));
}

export function hostOfPeer(p: PeerStatus): SessionHost {
  return { name: p.name, self: false, peer_id: p.id, reachable: true };
}

export interface HostPillProps {
  /** Undefined: this device. */
  value?: SessionHost;
  onChange: (host: SessionHost | undefined) => void;
}

/**
 * Where the new session runs (docs/MESH-MODE-DESIGN.md D6). Only there when
 * another device can take one; the default, and the only choice without
 * mesh mode, is this device.
 */
export function HostPill({ value, onChange }: HostPillProps) {
  const [open, setOpen] = useState(false);
  const { peers } = useMesh();
  const launchable = launchablePeers(peers);
  if (launchable.length === 0 && !value) return null;

  const remote = Boolean(value && !value.self);
  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={[pillStyles.trigger, remote ? pillStyles.modified : ""].filter(Boolean).join(" ")}
          data-launch-host={value?.name ?? ""}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={pillStyles.key}>on</span> {remote ? value!.name : "This device"}
          <Caret />
        </button>
      }
    >
      <div className={pillStyles.menu}>
        <button
          type="button"
          className={[pillStyles.option, !remote ? pillStyles.optionSelected : ""].filter(Boolean).join(" ")}
          onClick={() => {
            onChange(undefined);
            setOpen(false);
          }}
        >
          This device
        </button>
        {launchable.map((p) => (
          <button
            key={p.id}
            type="button"
            className={[pillStyles.option, value?.name === p.name ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => {
              onChange(hostOfPeer(p));
              setOpen(false);
            }}
          >
            <span className={pillStyles.optionLabel}>{p.name}</span>
            {p.rtt_ms !== undefined && <span className={pillStyles.optionDescription}>{p.rtt_ms} ms</span>}
          </button>
        ))}
      </div>
    </Popover>
  );
}
