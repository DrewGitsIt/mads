import { useState } from "react";
import { Select } from "../components/Select/Select";
import { CHANGE_TIERS, type ChangeSet, type ChangedFile } from "../router/types";
import { FileCounts, KindBadge, NewDot } from "../files/ChangeBits";
import {
  TIER_LABEL,
  basename,
  clipPrompt,
  dirname,
  isLowTier,
  railFiles,
  railStops,
  resolveRailTurn,
  stepRailStop,
  turnByN,
  type ChangesFocus,
  type RailStop,
  type RailTier,
} from "../files/changesModel";
import styles from "./RailChangesList.module.css";

export interface RailChangesListProps {
  changeSet: ChangeSet | null;
  loading?: boolean;
  error?: string;
  isUnseen: (f: ChangedFile) => boolean;
  /**
   * Opens the Changes tab: on a file (row click) or on the rail's tier and
   * turn (the footer link). Rows are inert and the link hidden without it.
   */
  onOpen?: (focus: ChangesFocus) => void;
}

/**
 * The Context Rail's Changes list (docs/mockups/changes-v2.html, rail): a
 * tier dropdown (default Source), a ‹ turn › selector that skips turns with
 * nothing in the tier and ends on a "Latest" stop that follows the newest
 * turn, the files by size (filename first), and a link into the Changes tab.
 * ContextRail owns the fetch so its tab badge has the count.
 */
export function RailChangesList({ changeSet, loading, error, isUnseen, onOpen }: RailChangesListProps) {
  const [tier, setTier] = useState<RailTier>("source");
  const [stop, setStop] = useState<RailStop>(null);

  if (!changeSet) {
    return <div className={styles.empty}>{loading || !error ? "Loading changes…" : `Couldn't load changes: ${error}`}</div>;
  }

  const cs = changeSet;
  const stops = railStops(cs, tier);
  // A stop that left the list (a refetch, or a tier with nothing in that turn) falls back to All turns.
  const current: RailStop = stops.includes(stop) ? stop : null;
  const i = stops.indexOf(current);
  const turnN = resolveRailTurn(cs, current);
  const turn = turnN !== null ? turnByN(cs.turns, turnN) : undefined;
  const files = railFiles(cs, tier, current);
  const unseen = files.filter(isUnseen).length;
  const focusTurn = turnN;

  const options = [
    { value: "all", label: `All files (${cs.files.length})` },
    ...CHANGE_TIERS.map((t) => ({
      value: t,
      label: `${TIER_LABEL[t]} (${cs.files.filter((f) => f.tier === t).length})`,
    })),
  ];

  function chooseTier(next: string) {
    const t = next as RailTier;
    setTier(t);
    if (!railStops(cs, t).includes(current)) setStop(null);
  }

  const tierWord = tier === "all" ? "" : `${TIER_LABEL[tier].toLowerCase()} `;

  return (
    <div className={styles.panel}>
      <div className={styles.ctl}>
        <Select value={tier} options={options} onChange={chooseTier} className={styles.tier} />
        {cs.turns.length > 0 && (
          <div className={styles.tsel}>
            <button
              type="button"
              className={styles.arrow}
              disabled={i <= 0}
              onClick={() => setStop(stepRailStop(stops, current, -1))}
              aria-label="Previous turn"
              title="Previous turn"
            >
              ‹
            </button>
            <div className={styles.mid} data-testid="rail-turn">
              {current === null ? (
                <span className={styles.tn}>All turns</span>
              ) : current === "latest" ? (
                <>
                  <span className={styles.latest}>● Latest</span>{" "}
                  <span className={styles.of}>following turn {turnN}</span>
                </>
              ) : (
                <>
                  <span className={styles.tn}>Turn {turnN}</span>{" "}
                  <span className={styles.of}>of {cs.turns.length}</span>
                </>
              )}
            </div>
            <button
              type="button"
              className={styles.arrow}
              disabled={i >= stops.length - 1}
              onClick={() => setStop(stepRailStop(stops, current, 1))}
              aria-label="Next turn"
              title="Next turn"
            >
              ›
            </button>
          </div>
        )}
        {current !== null && turn?.prompt && (
          <div className={styles.tprompt} title={turn.prompt}>
            “{clipPrompt(turn.prompt)}”
          </div>
        )}
      </div>

      <div className={styles.files}>
        {files.length === 0 ? (
          <div className={styles.empty}>
            {current === null ? `No ${tierWord}changes yet.` : `No ${tierWord}changes in this turn.`}
          </div>
        ) : (
          files.map((f) => {
            const dir = dirname(f.display);
            return (
              <button
                key={f.path}
                type="button"
                className={styles.row}
                data-dim={isLowTier(f) || undefined}
                disabled={!onOpen}
                title={f.path}
                onClick={onOpen ? () => onOpen({ path: f.path, tier, turn: focusTurn }) : undefined}
              >
                <NewDot show={isUnseen(f)} />
                <KindBadge change={f.change} />
                <span className={styles.p}>
                  <span className={styles.fn}>{basename(f.display)}</span>
                  {dir && (
                    <span className={styles.dir}>
                      <bdi>{dir}</bdi>
                    </span>
                  )}
                </span>
                <FileCounts file={f} />
              </button>
            );
          })
        )}
      </div>

      <div className={styles.foot}>
        <span className={styles.shown}>
          {files.length} shown
          {unseen > 0 && (
            <>
              {" · "}
              <NewDot show className={styles.footDot} /> {unseen} new
            </>
          )}
        </span>
        {onOpen && (
          <button type="button" className={styles.link} onClick={() => onOpen({ tier, turn: focusTurn })}>
            Open in Changes →
          </button>
        )}
      </div>
    </div>
  );
}
