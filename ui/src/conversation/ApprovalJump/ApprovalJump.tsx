import { useEffect, useRef, type RefObject } from "react";
import { FADE_OUT_MS, usePresence } from "../../components/Motion";
import type { PermissionRequest } from "../../router/types";
import { basename, summarize } from "../activity/toolDisplay";
import { useVisibleCards } from "../useVisibleCards";
import type { PermissionReveal } from "./permissionReveal";
import styles from "./ApprovalJump.module.css";

const TARGET_CAP = 40;
const FLASH_MS = 1000;
const PENDING_SELECTOR = '[data-permission-status="pending"]';

function truncate(s: string, cap: number): string {
  const line = s.split("\n")[0].trim();
  return line.length > cap ? `${line.slice(0, cap - 1)}…` : line;
}

/**
 * What a parked call would touch, in a few words: a file's basename, the
 * command's first line, else the same one-line summary a row would show.
 */
export function permissionTarget(req: PermissionRequest): string | undefined {
  const input =
    req.input && typeof req.input === "object" ? (req.input as Record<string, unknown>) : {};
  const str = (v: unknown) => (typeof v === "string" && v.trim() !== "" ? v : undefined);
  const path = str(input.file_path) ?? str(input.notebook_path) ?? str(input.path);
  if (path) return basename(path);
  const command = str(input.command);
  if (command) return truncate(command, TARGET_CAP);
  const summary = summarize({ name: req.tool, input: req.input });
  return summary === "no input" ? undefined : truncate(summary, TARGET_CAP);
}

/** "Approval needed: Edit main.go · audit the parser", or "3 approvals waiting". */
export function approvalLabel(
  requests: PermissionRequest[],
  subagentName: (req: PermissionRequest) => string | undefined,
): string {
  if (requests.length !== 1) return `${requests.length} approvals waiting`;
  const req = requests[0];
  const target = permissionTarget(req);
  const agent = subagentName(req);
  return `Approval needed: ${req.tool}${target ? ` ${target}` : ""}${agent ? ` · ${agent}` : ""}`;
}

function pendingIdOf(el: Element): string | undefined {
  return (el as HTMLElement).dataset.permissionId;
}

/** Restart-safe ~1s highlight: re-adding the attribute replays the animation. */
function flash(el: HTMLElement, timers: Map<HTMLElement, number>) {
  el.removeAttribute("data-flash");
  void el.offsetWidth;
  el.setAttribute("data-flash", "");
  window.clearTimeout(timers.get(el));
  timers.set(
    el,
    window.setTimeout(() => {
      el.removeAttribute("data-flash");
      timers.delete(el);
    }, FLASH_MS),
  );
}

export interface ApprovalJumpProps {
  /** Pending requests, oldest first. */
  pending: PermissionRequest[];
  /** Subagent display name for a request a subagent's call raised. */
  subagentName: (req: PermissionRequest) => string | undefined;
  /** Everywhere a card can render (main stream and dock). */
  scopeRef: RefObject<HTMLElement | null>;
  streamRef: RefObject<HTMLElement | null>;
  pinnedRef: RefObject<HTMLElement | null>;
  /** The latest reveal; the parent publishes it to collapsed groups. */
  reveal: PermissionReveal | null;
  onReveal: (id: string) => void;
}

/**
 * Floating "approval needed" pill over the bottom of the conversation: shown
 * while any pending permission card is off screen, turn live or not — a
 * background subagent can park a call long after the turn that spawned it
 * ended, its card far up the history. Click scrolls the main stream's copy
 * (which always exists; the dock's is a second view) into view, flashes it
 * and focuses its Allow button; further clicks walk the rest, oldest first.
 */
export function ApprovalJump({
  pending,
  subagentName,
  scopeRef,
  streamRef,
  pinnedRef,
  reveal,
  onReveal,
}: ApprovalJumpProps) {
  const visible = useVisibleCards(scopeRef, streamRef, pinnedRef, {
    selector: PENDING_SELECTOR,
    idAttribute: "data-permission-id",
    watchAttributes: ["data-permission-status"],
  });
  const offscreen = pending.filter((r) => !visible.has(r.id));
  const lastJumped = useRef<string | undefined>(undefined);
  const flashTimers = useRef(new Map<HTMLElement, number>());

  const jump = () => {
    // Cycle: the next off-screen card after the one last jumped to, wrapping
    // to the oldest. (A just-revealed card usually drops out of `offscreen`
    // on its own once it scrolls in; this covers clicks mid-scroll.)
    const after = pending.findIndex((r) => r.id === lastJumped.current);
    const target = offscreen.find((r) => pending.indexOf(r) > after) ?? offscreen[0] ?? pending[0];
    if (!target) return;
    lastJumped.current = target.id;
    onReveal(target.id);
  };

  // Runs after the commit that carries the reveal, by which point any
  // collapsed group holding the card has opened (it does so during render).
  useEffect(() => {
    if (!reveal) return;
    const stream = streamRef.current;
    const el = [...(stream?.querySelectorAll<HTMLElement>("[data-permission-id]") ?? [])].find(
      (e) => pendingIdOf(e) === reveal.id,
    );
    if (!el) return;
    el.scrollIntoView?.({ behavior: "smooth", block: "center" });
    flash(el, flashTimers.current);
    // preventScroll: focus's own instant scroll would cut the smooth one short.
    el.querySelector<HTMLElement>('[data-permission-action="allow"]')?.focus({
      preventScroll: true,
    });
  }, [reveal, streamRef]);

  useEffect(() => {
    const timers = flashTimers.current;
    return () => {
      for (const t of timers.values()) window.clearTimeout(t);
    };
  }, []);

  // Announced on what's pending, not on what's visible — scrolling a card in
  // and out of view shouldn't make a screen reader repeat itself.
  const announcement = pending.length > 0 ? approvalLabel(pending, subagentName) : "";
  // The pill fades out rather than vanishing; while it does, it keeps the
  // last thing it said rather than "0 approvals waiting".
  const { present, leaving } = usePresence(offscreen.length > 0, FADE_OUT_MS);
  const lastLabel = useRef("");
  if (offscreen.length > 0) lastLabel.current = approvalLabel(offscreen, subagentName);
  const label = lastLabel.current;

  return (
    <div className={styles.region} data-permission-jump>
      <span className={styles.srOnly} aria-live="polite" aria-atomic="true">
        {announcement}
      </span>
      {present && (
        <button
          type="button"
          className={styles.jump}
          title={label}
          onClick={jump}
          data-leaving={leaving || undefined}
          aria-hidden={leaving || undefined}
          tabIndex={leaving ? -1 : undefined}
        >
          <span className={styles.dot} aria-hidden="true" />
          <span className={styles.label}>{label}</span>
        </button>
      )}
    </div>
  );
}
