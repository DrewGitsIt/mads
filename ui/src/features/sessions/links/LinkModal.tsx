import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../../../components/Button/Button";
import type { OrchestrateStatus } from "../../../router/types";
import {
  CADENCES,
  CADENCE_COPY,
  ROLE_COPY,
  describeChange,
  leaderOf,
  loopThrough,
  swapBlockedReason,
  type EffectiveEdge,
} from "./linkDraft";
import type { LinkEditor } from "./useLinkEditor";
import styles from "./LinkModal.module.css";

/** A node's one-line status, as the rail's status line reads it. */
export interface NodeStatus {
  label: string;
  /** "needs" and "bad" tint the line; anything else reads quiet. */
  tone?: string;
}

export interface LinkModalProps {
  editor: LinkEditor;
  statusOf?: (sessionId: string) => NodeStatus | undefined;
}

// The sky's geometry. Nodes sit on three rows (holders, focus, subjects); a
// side with more nodes than fit widens the canvas, and the sky scrolls
// sideways rather than stacking rows whose lines would cross other nodes.
const NODE_W = 140;
const GAP = 12;
const EDGE_X = NODE_W / 2 + GAP;
const MIN_W = 620;
const H = 400;
const Y_UP = 70;
const Y_FOCUS = 200;
const Y_DOWN = 330;
const POP_W = 256;
/** How far below the sky the edge editor may hang: over the legend, never the footer. */
const POP_OVERHANG = 28;

interface Point {
  x: number;
  y: number;
}

function canvasWidth(n: number): number {
  return Math.max(MIN_W, (n - 1) * (NODE_W + GAP) + 2 * EDGE_X);
}

/** One side's nodes, spread over the width that side needs and centred in the canvas. */
function spread(n: number, y: number, w: number): Point[] {
  if (n === 1) return [{ x: w / 2, y }];
  const own = canvasWidth(n);
  const offset = (w - own) / 2;
  const step = (own - 2 * EDGE_X) / (n - 1);
  return Array.from({ length: n }, (_, i) => ({ x: offset + EDGE_X + i * step, y }));
}

function bezier(p0: number, p1: number, p2: number, p3: number, t: number): number {
  const u = 1 - t;
  return u * u * u * p0 + 3 * u * u * t * p1 + 3 * u * t * t * p2 + t * t * t * p3;
}

const ORCHESTRATE_NOTE: Partial<Record<OrchestrateStatus, string>> = {
  pending_restart: "Orchestration tools activate when the leading agent is next idle.",
  unsupported: "The leader's provider can't host orchestration tools.",
  failed: "Orchestration tools failed to start.",
};

function MuteIcon() {
  return (
    <svg
      className={styles.ico}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      data-mute-icon=""
    >
      <path d="M13.7 21a2 2 0 0 1-3.4 0" />
      <path d="M18.6 13A17.9 17.9 0 0 1 18 8" />
      <path d="M6.3 6.3A5.9 5.9 0 0 0 6 8c0 7-3 9-3 9h14" />
      <path d="M18 8a6 6 0 0 0-9.3-5" />
      <path d="m2 2 20 20" />
    </svg>
  );
}

const roleClass = (e: { role: string }) => (e.role === "leader" ? styles.lead : styles.mon);

/**
 * The link editor (docs/mockups/link-modal-constellation.html): centred on one
 * session, its holders above, its subjects below, one line per link. It
 * floats over the blurred conversation pane and is deliberately non-modal —
 * the rail beside it stays live and is how links get added.
 */
export function LinkModal({ editor, statusOf }: LinkModalProps) {
  const titleId = useId();
  const confirmTitleId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const skyRef = useRef<HTMLDivElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const confirmRef = useRef<HTMLDivElement>(null);
  const focus = editor.focus!;
  const { edges, nameOf, pick, confirm, saveErr, changes, loop, doubleLeader } = editor;

  // Focus moves into the dialog when it opens.
  useEffect(() => {
    rootRef.current?.focus();
  }, []);

  // Esc peels one layer: the confirm, then an open edge editor, then the
  // modal itself (which asks first when anything is unsaved). A key another
  // control already handled (the rail's search field) is left alone.
  const escRef = useRef<() => void>(() => undefined);
  escRef.current = () => {
    if (editor.confirm) editor.keepEditing();
    else if (editor.pick) editor.pickEdge(undefined);
    else editor.requestClose();
  };
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      e.preventDefault();
      escRef.current();
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (confirm) confirmRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [confirm]);

  // A freshly opened edge editor takes focus, so the keyboard lands on it.
  useEffect(() => {
    if (!pick) return;
    popRef.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  }, [pick]);

  const ups = edges.filter((e) => e.subject === focus);
  const downs = edges.filter((e) => e.granting === focus);
  const W = canvasWidth(Math.max(ups.length, downs.length, 1));

  // Re-centre the scroll on the focus whenever it moves. The edge editor
  // lives outside the scroll box (so it is never clipped by it) and follows
  // the scroll through `view`.
  const [view, setView] = useState({ left: 0, width: MIN_W });
  const syncView = () => {
    const sky = skyRef.current;
    if (sky) setView({ left: sky.scrollLeft, width: sky.clientWidth || MIN_W });
  };
  useLayoutEffect(() => {
    const sky = skyRef.current;
    if (sky) sky.scrollLeft = (sky.scrollWidth - sky.clientWidth) / 2;
    syncView();
  }, [focus, W]);

  // Keep the edge editor's bottom within reach: measured, since its height
  // depends on which notes it carries.
  useLayoutEffect(() => {
    const pop = popRef.current;
    if (!pop) return;
    const top = parseFloat(pop.dataset.top ?? "0");
    pop.style.top = `${Math.max(6, Math.min(top, H + POP_OVERHANG - pop.offsetHeight))}px`;
  });

  const pos = new Map<string, Point>();
  pos.set(focus, { x: W / 2, y: Y_FOCUS });
  spread(ups.length, Y_UP, W).forEach((p, i) => pos.set(ups[i].granting, p));
  spread(downs.length, Y_DOWN, W).forEach((p, i) => pos.set(downs[i].subject, p));

  const refusedKey = saveErr?.refused ? saveErr.key : undefined;
  const isBad = (e: EffectiveEdge) => Boolean(loopThrough(edges, e)) || (e.key === refusedKey && e.dirty);

  const geometry = (e: EffectiveEdge) => {
    const a = pos.get(e.granting)!;
    const b = pos.get(e.subject)!;
    const y0 = a.y + (e.granting === focus ? 28 : 22);
    const y1 = b.y - (e.subject === focus ? 28 : 22);
    const ym = (y0 + y1) / 2;
    // The badge sits near the outer node, so badges space out like the nodes do.
    const t = e.granting === focus ? 0.72 : 0.28;
    return {
      d: `M ${a.x} ${y0} C ${a.x} ${ym}, ${b.x} ${ym}, ${b.x} ${y1}`,
      badge: { x: bezier(a.x, a.x, b.x, b.x, t), y: bezier(y0, ym, ym, y1, t) },
    };
  };

  const cadenceClass = { per_turn: styles.perTurn, on_signal: styles.onSignal, manual_only: styles.manual };

  function node(id: string, edge?: EffectiveEdge) {
    const isFocus = id === focus;
    const p = pos.get(id)!;
    const up = edges.filter((e) => e.subject === id && e.granting !== focus).length;
    const down = edges.filter((e) => e.granting === id && e.subject !== focus).length;
    const more = isFocus ? "" : [up ? `▲${up}` : "", down ? `▼${down}` : ""].filter(Boolean).join(" ");
    const status = statusOf?.(id);
    const body = (
      <>
        <div className={styles.nm}>{nameOf(id)}</div>
        <div className={[styles.st, status?.tone === "needs" || status?.tone === "bad" ? styles.stWarn : ""].join(" ")}>
          {status && <span className={styles.stLabel}>{status.label}</span>}
          {more && (
            <span className={styles.more} aria-hidden="true">
              {more}
            </span>
          )}
        </div>
      </>
    );
    const style = { left: p.x, top: p.y };
    if (isFocus) {
      return (
        <div key={id} className={[styles.node, styles.focusNode].join(" ")} style={style} data-node={id} data-focus-node="">
          {body}
        </div>
      );
    }
    const outside = [up ? `${up} more holding it` : "", down ? `${down} more it holds` : ""].filter(Boolean).join(", ");
    return (
      <button
        key={id}
        type="button"
        className={[styles.node, edge?.dirty ? styles.addNode : ""].filter(Boolean).join(" ")}
        style={style}
        data-node={id}
        title={`Center on ${nameOf(id)}`}
        aria-label={`Center on ${nameOf(id)}${outside ? ` (${outside})` : ""}`}
        onClick={() => editor.recenter(id)}
      >
        <span className={styles.goto} aria-hidden="true">
          ⤢
        </span>
        {body}
      </button>
    );
  }

  const picked = edges.find((e) => e.key === pick);

  function edgeEditor(pe: EffectiveEdge) {
    const g = geometry(pe).badge;
    const left = Math.min(Math.max(g.x - view.left + 20, 8), view.width - POP_W - 8);
    const top = Math.max(g.y - 150, 6);
    const otherLeader = leaderOf(edges, pe.subject, pe.key);
    const swapWhy = swapBlockedReason(edges, pe, nameOf);
    const loopHere = loopThrough(edges, pe);
    const refused = pe.key === refusedKey && pe.dirty ? saveErr!.message : undefined;
    const status = pe.server?.link.orchestrate_status;
    const orchestrateNote =
      !pe.dirty && pe.role === "leader" && status ? ORCHESTRATE_NOTE[status] : undefined;
    const holder = nameOf(pe.granting);
    const label = `${holder} → ${nameOf(pe.subject)}`;
    return (
      <div
        ref={popRef}
        className={styles.pop}
        style={{ left, top }}
        data-top={top}
        role="group"
        aria-label={`Edit link ${label}`}
        data-edge-editor={pe.key}
        onClick={(e) => e.stopPropagation()}
      >
        <div className={styles.ph}>
          {pe.isNew && <span className={styles.newTag}>New link</span>} <b>{holder}</b> → <b>{nameOf(pe.subject)}</b>
        </div>
        {loopHere && (
          <div className={[styles.why, styles.whyBad].join(" ")}>
            This closes a loop of leaders. Pick Monitors, swap, or unlink.
          </div>
        )}
        {refused && <div className={[styles.why, styles.whyBad].join(" ")}>Couldn&apos;t save: {refused}</div>}
        {orchestrateNote && (
          <div className={styles.why}>
            {orchestrateNote}
            {status === "failed" && pe.server?.link.orchestrate_error ? ` ${pe.server.link.orchestrate_error}` : ""}
          </div>
        )}
        <div className={styles.k}>Role</div>
        <div className={styles.seg}>
          {(["leader", "monitor"] as const).map((role) => (
            <button
              key={role}
              type="button"
              className={[roleClass({ role }), pe.role === role ? styles.on : ""].filter(Boolean).join(" ")}
              aria-pressed={pe.role === role}
              disabled={role === "leader" && Boolean(otherLeader)}
              onClick={() => editor.setRole(pe.key, role)}
            >
              {ROLE_COPY[role].glyph} {ROLE_COPY[role].title}
            </button>
          ))}
        </div>
        {otherLeader && <div className={styles.why}>Already led by {nameOf(otherLeader.granting)}.</div>}
        <div className={styles.k} id={`${titleId}-cad`}>
          Updates to {holder}
        </div>
        <div role="radiogroup" aria-labelledby={`${titleId}-cad`}>
          {CADENCES.map((c) => (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={pe.cadence === c}
              className={[styles.o, pe.cadence === c ? styles.on : ""].filter(Boolean).join(" ")}
              onClick={() => editor.setCadence(pe.key, c)}
            >
              <span className={styles.r} aria-hidden="true" />
              <span>
                <span className={styles.ot}>
                  {c === "manual_only" && <MuteIcon />} {CADENCE_COPY[c].title}
                </span>
                <span className={styles.od}>{CADENCE_COPY[c].description}</span>
              </span>
            </button>
          ))}
        </div>
        <hr className={styles.hr} />
        <button
          type="button"
          className={styles.act}
          disabled={Boolean(swapWhy)}
          title={swapWhy}
          onClick={() => editor.swap(pe.key)}
        >
          ⇅ Swap direction
        </button>
        {swapWhy && <div className={styles.why}>{swapWhy}</div>}
        <button type="button" className={[styles.act, styles.actBad].join(" ")} onClick={() => editor.remove(pe.key)}>
          × Unlink
        </button>
      </div>
    );
  }

  const n = changes.length;
  const plural = (k: number, w: string) => `${k} ${w}${k === 1 ? "" : "s"}`;
  let footerNote: React.ReactNode = null;
  if (loop) {
    footerNote = (
      <span className={styles.invalid} role="alert">
        Can&apos;t save:{" "}
        {loop.map((id, i) => (
          <span key={i}>
            {i > 0 && <b className={styles.loopArrow}> ♛→ </b>}
            {nameOf(id)}
          </span>
        ))}{" "}
        is a loop of leaders. Change one link to Monitors or unlink it.
      </span>
    );
  } else if (doubleLeader) {
    footerNote = (
      <span className={styles.invalid} role="alert">
        Can&apos;t save: {nameOf(doubleLeader.subject)} would have two leaders,{" "}
        {doubleLeader.holders.map(nameOf).join(" and ")}. Change one to Monitors or unlink it.
      </span>
    );
  } else if (saveErr && n > 0) {
    footerNote = (
      <span className={styles.invalid} role="alert">
        Saved {saveErr.appliedCount} of {saveErr.total}. Couldn&apos;t save {saveErr.label}: {saveErr.message}
        {saveErr.refused ? " Change that link, then save again." : ""}
      </span>
    );
  } else if (n > 0) {
    footerNote = (
      <span className={styles.pending} title={changes.map((c) => describeChange(c, nameOf)).join("\n")}>
        {n} unsaved
      </span>
    );
  }

  const canSave = n > 0 && !loop && !doubleLeader && !editor.saving;
  const saveLabel = editor.saving
    ? "Saving…"
    : n === 0
      ? "No changes"
      : saveErr && !saveErr.refused
        ? "Retry"
        : `Save ${plural(n, "change")}`;

  const lineLegend = (cls: string) => (
    <svg viewBox="0 0 34 8" aria-hidden="true">
      <path className={[styles.wire, cls, styles.perTurn].join(" ")} d="M1 4 L33 4" />
    </svg>
  );

  return (
    <div
      ref={rootRef}
      className={styles.modal}
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      tabIndex={-1}
      onClick={(e) => e.stopPropagation()}
      data-link-modal=""
    >
      <div className={styles.mh}>
        <div className={styles.mhText}>
          <div className={styles.ttl} id={titleId}>
            Links · {nameOf(focus)}
          </div>
          {editor.trail.length > 1 ? (
            <nav className={styles.crumbs} aria-label="Centered on">
              {editor.trail.map((id, i) =>
                i === editor.trail.length - 1 ? (
                  <span key={id} className={styles.cur} aria-current="location">
                    {nameOf(id)}
                  </span>
                ) : (
                  <span key={id} className={styles.crumb}>
                    <button type="button" onClick={() => editor.recenter(id)}>
                      {nameOf(id)}
                    </button>
                    <span aria-hidden="true">›</span>
                  </span>
                ),
              )}
            </nav>
          ) : (
            <div className={styles.sub}>
              Click a session in the list to link it here. Click a node to center on it, a line&apos;s badge to edit it.
            </div>
          )}
        </div>
        <button type="button" className={styles.x} aria-label="Close" onClick={editor.requestClose}>
          ×
        </button>
      </div>

      <div className={styles.skyWrap}>
        <span className={styles.lane} style={{ top: 10 }}>
          Leads / monitors this
        </span>
        <span className={styles.lane} style={{ bottom: 8 }}>
          Led / monitored by this
        </span>
        <div
          ref={skyRef}
          className={styles.skyScroll}
          onScroll={syncView}
          onClick={(e) => {
            // A click on empty sky closes the edge editor.
            if (pick && !(e.target as HTMLElement).closest("button")) editor.pickEdge(undefined);
          }}
        >
          <div className={styles.sky} style={{ width: W, height: H }} data-sky="">
            <svg className={styles.wires} viewBox={`0 0 ${W} ${H}`} aria-hidden="true">
              {[...ups, ...downs].map((e) => (
                <path
                  key={e.key}
                  className={[styles.wire, roleClass(e), cadenceClass[e.cadence]].join(" ")}
                  d={geometry(e).d}
                  data-wire={e.key}
                  data-role={e.role}
                  data-cadence={e.cadence}
                />
              ))}
              {downs.length === 0 && (
                <path className={[styles.wire, styles.ghostWire].join(" ")} d={`M ${W / 2} ${Y_FOCUS + 28} L ${W / 2} ${Y_DOWN - 14}`} />
              )}
            </svg>
            {node(focus)}
            {ups.map((e) => node(e.granting, e))}
            {downs.map((e) => node(e.subject, e))}
            {ups.length === 0 && (
              <div className={[styles.node, styles.ghostNode].join(" ")} style={{ left: W / 2, top: Y_UP }}>
                Nothing leads or monitors this
              </div>
            )}
            {downs.length === 0 && (
              <div className={[styles.node, styles.ghostNode].join(" ")} style={{ left: W / 2, top: Y_DOWN }}>
                ← Click sessions in the list to link them here
              </div>
            )}
            {[...ups, ...downs].map((e) => {
              const g = geometry(e).badge;
              const bad = isBad(e);
              const role = ROLE_COPY[e.role];
              const cad = CADENCE_COPY[e.cadence].title;
              return (
                <button
                  key={e.key}
                  type="button"
                  className={[
                    styles.eb,
                    roleClass(e),
                    pick === e.key ? styles.picked : "",
                    e.dirty ? styles.dirty : "",
                    bad ? styles.bad : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  style={{ left: g.x, top: g.y }}
                  data-edge={e.key}
                  data-dirty={e.dirty ? "" : undefined}
                  data-bad={bad ? "" : undefined}
                  aria-expanded={pick === e.key}
                  aria-label={`${nameOf(e.granting)} ${role.verb} ${nameOf(e.subject)}, ${cad}${e.dirty ? ", unsaved" : ""}${bad ? ", needs a change" : ""}. Edit link`}
                  title={bad ? (e.key === refusedKey ? "This link couldn't be saved" : "This link closes a loop of leaders") : "Edit this link"}
                  onClick={() => editor.pickEdge(pick === e.key ? undefined : e.key)}
                >
                  <span className={styles.g} aria-hidden="true">
                    {role.glyph}
                  </span>
                  {e.cadence === "manual_only" ? <MuteIcon /> : <span aria-hidden="true">{cad}</span>}
                </button>
              );
            })}
          </div>
        </div>
        {picked && edgeEditor(picked)}
      </div>

      <div className={styles.legend} aria-hidden="true">
        <span>
          {lineLegend(styles.lead)}♛ Leads: flows down
        </span>
        <span>
          {lineLegend(styles.mon)}◉ Monitors: flows up
        </span>
        <span>fast = Follow · slow = Signals · still = Muted</span>
      </div>

      <div className={styles.mf}>
        <div className={styles.mfNote}>{footerNote}</div>
        <Button variant="secondary" onClick={editor.requestClose}>
          Cancel
        </Button>
        <Button variant="primary" disabled={!canSave} onClick={() => void editor.save()}>
          {saveLabel}
        </Button>
      </div>

      {confirm && (
        <div className={styles.confirm}>
          <div ref={confirmRef} className={styles.confirmBox} role="alertdialog" aria-modal="true" aria-labelledby={confirmTitleId}>
            <div className={styles.confirmTitle} id={confirmTitleId}>
              {editor.appliedSome ? "Some changes are already saved. " : ""}Discard {plural(n, "unsaved change")}?
            </div>
            <ul className={styles.confirmList}>
              {changes.map((c) => (
                <li key={c.key}>{describeChange(c, nameOf)}</li>
              ))}
            </ul>
            <div className={styles.confirmRow}>
              <Button variant="secondary" onClick={editor.keepEditing}>
                Keep editing
              </Button>
              <Button variant="primary" className={styles.danger} onClick={editor.discard}>
                Discard
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
