import { useEffect, useState } from "react";
import type { CSSProperties } from "react";
import { Popover } from "../../../components/Popover/Popover";
import type { RouterClient } from "../../../router/client";
import type { Grant, LinkCadence, OrchestrateStatus } from "../../../router/types";
import pillStyles from "../launchpad/pills/pills.module.css";
import styles from "./SessionRail.module.css";

const CADENCE_OPTIONS: { value: LinkCadence; label: string; description: string }[] = [
  {
    value: "per_turn",
    label: "Follow intently",
    // Honest about digestwatch's batching — "per turn" would overstate it.
    description: "Debounced digest after each burst of activity.",
  },
  {
    value: "on_signal",
    label: "Wake on signals",
    description: "Digest only when it needs attention — a permission request, a crash, a task done or blocked.",
  },
  {
    value: "manual_only",
    label: "Catch up on request",
    description: "No automatic digests; the agent can ask for a summary.",
  },
];

/** Matches .linkOptions' width; used to keep the flyout on-screen. */
const FLYOUT_WIDTH = 224;
/** Rough flyout height — enough to decide whether it should open upward. */
const FLYOUT_HEIGHT_ESTIMATE = 260;

/**
 * Fixed placement under (or, near the window's bottom edge, over) the
 * trigger. The rail is a scroll container, so an absolutely-positioned
 * flyout would be clipped by it or scroll it sideways.
 */
function flyoutPlacement(trigger: DOMRect): CSSProperties {
  const gap = 4;
  const left = Math.max(8, Math.min(trigger.left, window.innerWidth - FLYOUT_WIDTH - 8));
  const openUp = trigger.bottom + gap + FLYOUT_HEIGHT_ESTIMATE > window.innerHeight;
  return openUp
    ? { position: "fixed", left, top: "auto", bottom: window.innerHeight - trigger.top + gap }
    : { position: "fixed", left, top: trigger.bottom + gap };
}

/** Badge suffix for a non-default cadence, so it reads at a glance. */
const CADENCE_STATE: Partial<Record<LinkCadence, string>> = {
  on_signal: "· ON SIGNAL",
  manual_only: "· MANUAL",
};

const ORCHESTRATE_NOTE: Partial<Record<OrchestrateStatus, string>> = {
  pending_restart: "Orchestration tools arrive the next time the leader restarts.",
  unsupported: "The leader's provider can't host orchestration tools.",
};

export interface LinkOptionsMenuProps {
  /** The subject row's role badge (LINKED / WATCHED) — doubles as the trigger. */
  badge: string;
  subjectName: string;
  linkId: string;
  grantingSessionId: string;
  subjectSessionId: string;
  grants: Grant[];
  cadence: LinkCadence;
  orchestrateStatus?: OrchestrateStatus;
  routerClient: RouterClient;
  onUnlinkRequest: () => void;
}

/**
 * Everything specific to one link, behind the nested row's role badge: the
 * update policy (PUT /links/{id}) and Unlink. Keeps the row itself to one
 * badge-sized control so it fits the rail. Grants are resent unchanged on a
 * cadence change: store.Links.Update replaces the whole grant set and rejects
 * an empty one.
 */
export function LinkOptionsMenu({
  badge,
  subjectName,
  linkId,
  grantingSessionId,
  subjectSessionId,
  grants,
  cadence,
  orchestrateStatus,
  routerClient,
  onUnlinkRequest,
}: LinkOptionsMenuProps) {
  const [placement, setPlacement] = useState<CSSProperties>();
  const [pending, setPending] = useState(false);
  const open = placement !== undefined;
  const close = () => setPlacement(undefined);

  // Fixed coordinates go stale as soon as anything scrolls; close rather
  // than float detached from the row.
  useEffect(() => {
    if (!open) return;
    const dismiss = () => setPlacement(undefined);
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [open]);
  const note = orchestrateStatus ? ORCHESTRATE_NOTE[orchestrateStatus] : undefined;

  async function handleSelect(next: LinkCadence) {
    if (next === cadence) {
      close();
      return;
    }
    setPending(true);
    try {
      await routerClient.updateLink(linkId, {
        granting_session_id: grantingSessionId,
        subject_session_id: subjectSessionId,
        grants,
        cadence: next,
      });
    } finally {
      setPending(false);
      close();
    }
  }

  return (
    <Popover
      open={open}
      onClose={close}
      className={styles.linkOptions}
      style={placement}
      anchor={
        <button
          type="button"
          className={[styles.linkBadge, styles.linkOptionsTrigger].join(" ")}
          aria-label={`Link options for ${subjectName}`}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={pending}
          onClick={(e) => {
            e.stopPropagation();
            if (open) close();
            else setPlacement(flyoutPlacement(e.currentTarget.getBoundingClientRect()));
          }}
        >
          {badge}
          {CADENCE_STATE[cadence] && <span className={styles.linkOptionsState}>{CADENCE_STATE[cadence]}</span>}
        </button>
      }
    >
      {/* Clicks inside the flyout must not bubble to the row and open it. */}
      <div className={pillStyles.menu} role="menu" onClick={(e) => e.stopPropagation()}>
        <div className={pillStyles.groupLabel}>Updates from this session</div>
        {CADENCE_OPTIONS.map((opt) => (
          <button
            key={opt.value}
            type="button"
            role="menuitemradio"
            aria-checked={opt.value === cadence}
            className={[pillStyles.option, opt.value === cadence ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => void handleSelect(opt.value)}
          >
            <span className={pillStyles.optionLabel}>{opt.label}</span>
            <span className={pillStyles.optionDescription}>{opt.description}</span>
          </button>
        ))}
        {note && <p className={styles.linkOptionsNote}>{note}</p>}
        <div className={styles.linkOptionsDivider} role="separator" />
        <button
          type="button"
          role="menuitem"
          className={[pillStyles.option, styles.linkOptionsDanger].join(" ")}
          onClick={() => {
            // Close first — the confirm dialog also listens for Escape.
            close();
            onUnlinkRequest();
          }}
        >
          Unlink…
        </button>
      </div>
    </Popover>
  );
}
