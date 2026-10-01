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
export function flyoutPlacement(trigger: DOMRect): CSSProperties {
  const gap = 4;
  const left = Math.max(8, Math.min(trigger.left, window.innerWidth - FLYOUT_WIDTH - 8));
  const openUp = trigger.bottom + gap + FLYOUT_HEIGHT_ESTIMATE > window.innerHeight;
  return openUp
    ? { position: "fixed", left, top: "auto", bottom: window.innerHeight - trigger.top + gap }
    : { position: "fixed", left, top: trigger.bottom + gap };
}

/**
 * Whether a link is in a state the user should see without opening anything.
 *
 * Only trouble qualifies. The trigger used to spell out the cadence too
 * ("LINKED · ON SIGNAL") and the row carried a second pill for the
 * orchestrate state beside it, which on a nested card — already indented,
 * already the quieter row — put two shouted lines of configuration under a
 * one-line status. Cadence is a setting, not news: the flyout shows it as the
 * checked option, which is where a setting belongs. A degraded orchestrate
 * grant IS news (the leader's tools are not there and will not arrive on
 * their own), so it keeps a mark on the trigger and its sentence inside.
 *
 * pending_restart is deliberately not news: the tools mount at the leader's
 * next idle moment without anyone doing anything.
 */
function isDegraded(status?: OrchestrateStatus): boolean {
  return status === "unsupported" || status === "failed";
}

const ORCHESTRATE_NOTE: Partial<Record<OrchestrateStatus, string>> = {
  // The runner mounts them at the leader's next quiet turn boundary (idle,
  // nothing queued), resuming the same conversation, not at a restart.
  pending_restart: "Orchestration tools activate when the leading agent is next idle.",
  unsupported: "The leader's provider can't host orchestration tools.",
  failed: "Orchestration tools failed to start.",
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
  /** Why the tools failed to start, appended to the "failed" note. */
  orchestrateError?: string;
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
  orchestrateError,
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
  const baseNote = orchestrateStatus ? ORCHESTRATE_NOTE[orchestrateStatus] : undefined;
  const note =
    baseNote && orchestrateStatus === "failed" && orchestrateError ? `${baseNote} ${orchestrateError}` : baseNote;

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
          // The mark on the trigger says "something is wrong here"; this says
          // what, on hover and to a screen reader, without the label itself
          // becoming a sentence that changes identity with the link's state.
          title={isDegraded(orchestrateStatus) ? note : undefined}
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
          {isDegraded(orchestrateStatus) && (
            <span className={styles.linkBadgeAlert} aria-hidden="true" />
          )}
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
