import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type {
  CompactStatus,
  SessionReport,
} from "../../../router/types";
import { useElapsedNow } from "../../../utils/relativeTime";
import { formatElapsed } from "../../activity/toolDisplay";
import { formatTokens } from "../../Dock/dockLabels";
import {
  formatBasis,
  formatResetTime,
  formatUsd,
  meterTone,
  percent,
  promptTokens,
} from "./usageFormat";
import styles from "./UsageCard.module.css";

/** The Runner's near-limit trigger (services/session/internal/runner/compact.go compactOccupancyThreshold). */
const AUTO_COMPACT_PCT = 85;

/**
 * Control-plane events after which the report has moved: the agent changed
 * state, a compaction pass started/finished, or a turn's usage landed.
 * session.usage_updated is the one that carries the turn's numbers —
 * turn_ended and the idle agent_state go out before the Runner writes them.
 */
const REFRESH_EVENTS = [
  "session.turn_ended",
  "session.agent_state",
  "session.compact",
  "session.usage_updated",
];

export interface UsageCardProps {
  routerClient: RouterClient;
  sessionId: string;
  /** The context window to measure against when the provider never reported one. */
  fallbackWindow: number;
  /** Compacts through the conversation's Compact-now path. Absent → no link. */
  onCompact?: () => void;
}

/**
 * The /usage card, layout B "Ledger" (docs/mockups/session-commands.html §1):
 * one narrow column of Health, Context, Tokens · session, then Plan limits
 * or Spend. A section the report doesn't carry is simply absent. Refetches
 * on the session's turn/agent-state/compact events while open; relative
 * times tick on the shared 1s clock.
 */
export function UsageCard({
  routerClient,
  sessionId,
  fallbackWindow,
  onCompact,
}: UsageCardProps) {
  const [report, setReport] = useState<SessionReport>();
  const [error, setError] = useState<string>();
  const [compacting, setCompacting] = useState(false);
  const alive = useRef(true);
  const inflight = useRef(false);
  const again = useRef(false);

  // One fetch at a time; events landing mid-fetch coalesce into one more.
  const load = useCallback(async () => {
    if (inflight.current) {
      again.current = true;
      return;
    }
    inflight.current = true;
    try {
      do {
        again.current = false;
        const r = await routerClient.getSessionReport(sessionId);
        if (!alive.current) return;
        setReport(r);
        setError(undefined);
      } while (again.current);
    } catch (err) {
      if (alive.current)
        setError(err instanceof Error ? err.message : String(err));
    } finally {
      inflight.current = false;
    }
  }, [routerClient, sessionId]);

  useEffect(() => {
    alive.current = true;
    void load();
    const unsubscribe = subscribeToEvents(
      { sessionId, types: REFRESH_EVENTS },
      (envelope) => {
        if (envelope.type === "session.compact") {
          const status = envelope.payload as Partial<CompactStatus> | undefined;
          setCompacting(status?.phase === "start");
        }
        void load();
      },
    );
    return () => {
      alive.current = false;
      unsubscribe();
    };
  }, [sessionId, load]);

  // Plan limits are the login's, shared by its sessions: another session's
  // newer reading changes this card too.
  const plan = report?.billing.mode === "plan";
  useEffect(() => {
    if (!plan) return;
    return subscribeToEvents({ types: ["session.usage_updated"] }, () => void load());
  }, [plan, load]);

  const now = useElapsedNow();

  if (!report) {
    return (
      <div className={styles.ledger} data-testid="usage-card" data-card-fit>
        <div className={styles.placeholder}>
          {error ? `Couldn't load usage: ${error}` : "Loading…"}
        </div>
      </div>
    );
  }

  const ago = (iso: string | undefined) => {
    const t = iso ? Date.parse(iso) : NaN;
    return Number.isNaN(t) ? "" : formatElapsed(now - t);
  };

  const { health, context, tokens, limits, limits_at, cost } = report;
  const since = ago(health.since);
  const up = ago(health.process_started_at);
  const last = ago(health.last_event_at);
  const harness = [health.harness, health.version].filter(Boolean).join(" ");

  return (
    <div className={styles.ledger} data-testid="usage-card" data-card-fit>
      <Section title="Health" />
      <Row k="Agent">
        <span
          className={styles.dot}
          data-state={health.state}
          aria-hidden="true"
        />{" "}
        {health.state}
        {since && ` · ${since}`}
      </Row>
      {up && (
        <Row k="Process · last event">
          up {up}
          {last && ` · ${last} ago`}
        </Row>
      )}
      {harness && (
        <Row k="Harness">
          {harness}
          {health.preflight_ok === true && (
            <span className={styles.ok} aria-label="preflight ok">
              {" "}
              ✓
            </span>
          )}
          {health.preflight_ok === false && (
            <span className={styles.bad} aria-label="preflight failed">
              {" "}
              ✕
            </span>
          )}
        </Row>
      )}

      {context && (
        <ContextSection
          used={context.used}
          window={context.window || fallbackWindow}
          canCompact={context.can_compact}
          compacting={compacting}
          onCompact={onCompact}
        />
      )}

      {tokens && (
        <>
          <Section title="Tokens · session" />
          <Row k="In / out">
            {formatTokens(promptTokens(tokens))} / {formatTokens(tokens.output)}
          </Row>
          {(tokens.cache_read ?? 0) > 0 && (
            <Row k="From cache">
              {formatTokens(tokens.cache_read!)} (
              {percent(tokens.cache_read!, promptTokens(tokens))}%)
            </Row>
          )}
          {tokens.delegates && (
            <Row k="↳ delegates">
              {formatTokens(promptTokens(tokens.delegates))} /{" "}
              {formatTokens(tokens.delegates.output)}
            </Row>
          )}
          <Row k="Turns">{tokens.turns}</Row>
        </>
      )}

      {limits && limits.length > 0 && (
        <>
          <Section title={limitsTitle(limits_at, now)} />
          {limits.map((l) => {
            const pct = Math.round(l.used_pct);
            const resets = formatResetTime(l.resets_at, now);
            return (
              <div key={l.id} className={styles.lmeter}>
                <span className={styles.k}>{l.label}</span>
                <Meter pct={pct} label={`${l.label} limit`} />
                <span className={styles.v}>
                  {pct}%{resets && ` · resets ${resets}`}
                </span>
              </div>
            );
          })}
        </>
      )}

      {cost && (
        <>
          <Section title="Spend · session" />
          <Row k={`≈ Cost (${formatBasis(cost.basis)})`}>
            {formatUsd(cost.usd)}
          </Row>
        </>
      )}
    </div>
  );
}

function ContextSection({
  used,
  window,
  canCompact,
  compacting,
  onCompact,
}: {
  used: number;
  window: number;
  canCompact: boolean;
  compacting: boolean;
  onCompact?: () => void;
}) {
  const pct = percent(used, window);
  return (
    <>
      <Section title="Context" />
      <div className={styles.lmeter}>
        <Meter pct={pct} label="Context used" />
        <span className={styles.v}>
          {formatTokens(used)} / {formatTokens(window)}
        </span>
      </div>
      {canCompact && (
        <Row k={`Auto-compact at ${AUTO_COMPACT_PCT}%`}>
          {compacting ? (
            <span className={styles.muted}>Compacting…</span>
          ) : (
            onCompact && (
              <button
                type="button"
                className={styles.link}
                onClick={() => onCompact()}
              >
                Compact now
              </button>
            )
          )}
        </Row>
      )}
    </>
  );
}

/** Readings older than this say how old they are: no session on the login has heard since. */
const LIMITS_STALE_MS = 15 * 60_000;

function limitsTitle(at: string | undefined, now: number): string {
  const t = at ? Date.parse(at) : NaN;
  if (Number.isNaN(t) || now - t < LIMITS_STALE_MS) return "Plan limits";
  return `Plan limits · as of ${formatElapsed(now - t)} ago`;
}

function Section({ title }: { title: string }) {
  return (
    <div className={styles.lsec} role="heading" aria-level={3}>
      {title}
    </div>
  );
}

function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className={styles.row}>
      <span className={styles.k}>{k}</span>
      <span className={styles.v}>{children}</span>
    </div>
  );
}

function Meter({ pct, label }: { pct: number; label: string }) {
  return (
    <div
      className={styles.meter}
      data-tone={meterTone(pct)}
      role="meter"
      aria-label={label}
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <i style={{ width: `${pct}%` }} />
    </div>
  );
}
