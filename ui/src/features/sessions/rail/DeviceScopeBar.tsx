import { useState } from "react";
import { Caret } from "../../../components/Caret/Caret";
import { Popover } from "../../../components/Popover/Popover";
import { effectiveScope, scopePeer, setMeshScope, useMesh, type MeshScope } from "../../../router/mesh";
import type { PeerStatus } from "../../../router/types";
import styles from "./SessionRail.module.css";

/**
 * Which devices' sessions the list shows (docs/MESH-MODE-DESIGN.md D5):
 * This device, All devices, or any one other device. Only there once a
 * device has been added: a single-device install never sees it, and the
 * default is this device, so nothing changes until the user asks.
 */
export function DeviceScopeBar() {
  const mesh = useMesh();
  const [open, setOpen] = useState(false);
  const visible = mesh.peers.filter((p) => !p.hidden);
  if (visible.length === 0) return null;
  const scope = effectiveScope(mesh);
  const onlyPeer = scopePeer(scope);
  const label = scope === "self" ? "This device" : scope === "all" ? "All devices" : onlyPeer!;
  // A device that can't be read right now, among those the list is showing.
  const shown = scope === "all" ? visible : visible.filter((p) => p.name === onlyPeer);
  const down = shown.filter((p) => p.reachability !== "connected" && p.reachability !== "unknown");

  function choose(next: MeshScope) {
    setMeshScope(next);
    setOpen(false);
  }

  const option = (value: MeshScope, text: string, peer?: PeerStatus) => (
    <button
      key={value}
      type="button"
      role="menuitemradio"
      aria-checked={scope === value}
      className={[styles.deviceOption, scope === value ? styles.deviceOptionOn : ""].filter(Boolean).join(" ")}
      onClick={() => choose(value)}
    >
      {peer && <span className={[styles.deviceDot, dotClass(peer)].join(" ")} aria-hidden="true" />}
      {text}
      {peer && peer.reachability !== "connected" && (
        <span className={styles.deviceOptionNote}>{shortState(peer)}</span>
      )}
    </button>
  );

  return (
    <div className={styles.filterBar} data-device-scope={scope}>
      <Popover
        open={open}
        onClose={() => setOpen(false)}
        anchor={
          <button
            type="button"
            className={[styles.chip, scope !== "self" ? styles.chipOn : ""].filter(Boolean).join(" ")}
            aria-haspopup="menu"
            aria-expanded={open}
            aria-label={`Show sessions from: ${label}`}
            onClick={() => setOpen((o) => !o)}
          >
            {label}
            <Caret />
          </button>
        }
      >
        <div className={styles.deviceMenu} role="menu" aria-label="Show sessions from">
          {option("self", "This device")}
          {option("all", "All devices")}
          <div className={styles.deviceMenuRule} role="separator" />
          {visible.map((p) => option(`peer:${p.name}`, p.name, p))}
        </div>
      </Popover>
      {down.length > 0 && (
        <span className={styles.deviceDown} title={down.map(describePeer).join("\n")} data-devices-down={down.length}>
          {down.length === 1 ? `${down[0].name} ${shortState(down[0])}` : `${down.length} devices unavailable`}
        </span>
      )}
    </div>
  );
}

function dotClass(p: PeerStatus): string {
  switch (p.reachability) {
    case "connected":
      return styles.deviceDotOk;
    case "unreachable":
      return styles.deviceDotBad;
    case "unknown":
      return "";
    default:
      return styles.deviceDotWarn;
  }
}

function shortState(p: PeerStatus): string {
  switch (p.reachability) {
    case "denied":
      return "denied";
    case "version_mismatch":
      return "needs update";
    case "unpaired":
      return "not paired";
    case "unknown":
      return "checking";
    default:
      return "offline";
  }
}

function describePeer(p: PeerStatus): string {
  return `${p.name}: ${p.reachability.replace("_", " ")}${p.reason ? ` (${p.reason})` : ""}`;
}
