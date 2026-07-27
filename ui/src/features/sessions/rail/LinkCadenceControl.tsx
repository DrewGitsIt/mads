import { useState } from "react";
import { Popover } from "../../../components/Popover/Popover";
import type { RouterClient } from "../../../router/client";
import type { Grant, LinkCadence } from "../../../router/types";
import pillStyles from "../launchpad/pills/pills.module.css";

const CADENCE_OPTIONS: { value: LinkCadence; label: string; description: string }[] = [
  {
    value: "per_turn",
    label: "Follow intently",
    description: "Digest after each burst of activity.",
  },
  {
    value: "manual_only",
    label: "Catch up on request",
    description: "No automatic digests; the agent can ask for a summary.",
  },
];

/** Honest, debounce-aware badge text — "per turn" would overstate the digestwatch batching. */
const CADENCE_BADGE: Record<LinkCadence, string> = {
  per_turn: "DEBOUNCED DIGESTS",
  manual_only: "MANUAL ONLY",
};

export interface LinkCadenceControlProps {
  linkId: string;
  grantingSessionId: string;
  subjectSessionId: string;
  grants: Grant[];
  cadence: LinkCadence;
  routerClient: RouterClient;
}

/**
 * Rail badge that doubles as the cadence editor for an existing link — clicking it
 * opens a Popover to change cadence via PUT /links/{id}. Grants are resent unchanged:
 * store.Links.Update replaces the whole grant set and rejects an empty one.
 */
export function LinkCadenceControl({
  linkId,
  grantingSessionId,
  subjectSessionId,
  grants,
  cadence,
  routerClient,
}: LinkCadenceControlProps) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);

  async function handleSelect(next: LinkCadence) {
    if (next === cadence) {
      setOpen(false);
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
      setOpen(false);
    }
  }

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={pillStyles.trigger}
          disabled={pending}
          onClick={(e) => {
            e.stopPropagation();
            setOpen((o) => !o);
          }}
        >
          {CADENCE_BADGE[cadence]}
        </button>
      }
    >
      <div className={pillStyles.menu}>
        {CADENCE_OPTIONS.map((opt) => (
          <button
            key={opt.value}
            type="button"
            className={[pillStyles.option, opt.value === cadence ? pillStyles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={(e) => {
              e.stopPropagation();
              void handleSelect(opt.value);
            }}
          >
            <span className={pillStyles.optionLabel}>{opt.label}</span>
            <span className={pillStyles.optionDescription}>{opt.description}</span>
          </button>
        ))}
      </div>
    </Popover>
  );
}
