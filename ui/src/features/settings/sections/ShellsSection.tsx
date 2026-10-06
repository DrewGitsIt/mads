import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { Button } from "../../../components/Button/Button";
import { requestOpenSession } from "../../../appNavigation";
import { useShellDockState } from "../../../conversation/ShellDock/dockState";
import { EndShellDialog } from "../../../conversation/ShellDock/EndShellDialog";
import { useLiveShells } from "../../../conversation/ShellDock/useLiveShells";
import type { ShellActivity, ShellHostBridge, ShellInfo } from "../../../conversation/TerminalRenderer/ptyBridge";
import { displayPath } from "../../../files/displayPath";
import { formatAbsoluteStamp, formatRelativeStamp, useRelativeNow } from "../../../utils/relativeTime";
import { useSessionsList } from "../../sessions/state/useSessionsList";
import styles from "./ShellsSection.module.css";

/** docs/SHELL-DOCK-DESIGN.md D5: the host is asked every 5 s, and only while this section shows. */
export const INSPECT_INTERVAL_MS = 5_000;

/**
 * Whether `ref`'s element is on screen. Settings scrolls an internal column, and
 * an IntersectionObserver with the default root still sees through that clip.
 * It reports what the user can see, which the scroll-spy's single "active
 * section" doesn't: a short Shells section can be fully on screen while the
 * spy names its neighbour. Without IntersectionObserver (no layout engine)
 * the section counts as visible.
 */
function useOnScreen(ref: RefObject<Element | null>): boolean {
  const [visible, setVisible] = useState(() => typeof IntersectionObserver === "undefined");
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver((entries) => {
      const last = entries[entries.length - 1];
      if (last) setVisible(last.isIntersecting);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return visible;
}

/**
 * What runs in each shell, read from the shell host: once when the section
 * comes on screen (and whenever the set of shells changes), then every 5 s,
 * and not at all while hidden, unmounted or with no live shell. Unlike list(),
 * inspect connects to the host, so with nothing to ask about it is never called.
 */
function useShellActivity(active: boolean, shellIds: string): Map<string, ShellActivity> {
  const [activity, setActivity] = useState<Map<string, ShellActivity>>(new Map());
  useEffect(() => {
    const host = window.mads?.shell;
    if (!active || !host || shellIds === "") return;
    let cancelled = false;
    let inFlight = false;
    const poll = () => {
      // A slow host mustn't see a second ask queued behind the first.
      if (inFlight) return;
      inFlight = true;
      host
        .inspect()
        .then((all) => {
          if (!cancelled) setActivity(new Map(all.map((a) => [a.shellId, a])));
        })
        .catch(() => undefined)
        .finally(() => {
          inFlight = false;
        });
    };
    poll();
    const timer = setInterval(poll, INSPECT_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [active, shellIds]);
  return activity;
}

/** "tail -f x", "tail -f x (+2 more)", or "idle". */
function describeActivity(activity: ShellActivity | undefined): string {
  if (!activity) return "…";
  if (!activity.busy) return "idle";
  const first = activity.processes[0]?.command ?? activity.foreground ?? "busy";
  const more = activity.count - 1;
  return more > 0 ? `${first} (+${more} more)` : first;
}

function formatMemory(rssKb: number): string {
  return `${Math.round(rssKb / 1024)} MB`;
}

interface ShellRowProps {
  shell: ShellInfo;
  /** The session's name; undefined once the session no longer exists. */
  sessionName: string | undefined;
  activity: ShellActivity | undefined;
  now: number;
  onEnd: (shell: ShellInfo) => void;
}

function ShellRow({ shell, sessionName, activity, now, onEnd }: ShellRowProps) {
  // This window's dock state for the session: whether its dock is showing.
  const { open } = useShellDockState(shell.sessionId);
  const startedIso = new Date(shell.startedAt).toISOString();
  return (
    <tr>
      <td>
        {sessionName !== undefined ? (
          <button
            type="button"
            className={styles.link}
            onClick={() => requestOpenSession(shell.sessionId)}
          >
            {sessionName || "Untitled session"}
          </button>
        ) : (
          <span className={styles.gone}>session gone</span>
        )}
      </td>
      <td className={styles.mono} title={shell.cwd}>
        {shell.cwd ? displayPath(shell.cwd) : "—"}
      </td>
      <td title={formatAbsoluteStamp(startedIso)}>{formatRelativeStamp(startedIso, now)}</td>
      {/* Settings is on screen, so no dock is: this is whether the session's
          dock comes back open when you return to it. */}
      <td title={open ? "Opens with its session" : "Hidden; the shell keeps running"}>{open ? "open" : "hidden"}</td>
      <td className={styles.mono}>{describeActivity(activity)}</td>
      <td className={styles.num}>{activity ? formatMemory(activity.rssKb) : "…"}</td>
      <td>
        <Button size="sm" onClick={() => onEnd(shell)}>
          End
        </Button>
      </td>
    </tr>
  );
}

/**
 * Settings → Shells (docs/SHELL-DOCK-DESIGN.md D8): every live dock shell, with
 * what runs in it, and the way to end one. It is not a Config section — no
 * fields, no profile diff, no restart badge. The shell list comes from main's
 * cache (useLiveShells), so merely having this page open never forks the host;
 * only inspect does, and only while shells exist and the section is on screen.
 */
export function ShellsSection() {
  const host = typeof window !== "undefined" ? window.mads?.shell : undefined;
  // Split so a plain browser never opens the sessions fetch and event stream.
  if (!host) return <p className={styles.empty}>Shells need the desktop app.</p>;
  return <ShellInventory host={host} />;
}

function ShellInventory({ host }: { host: ShellHostBridge }) {
  const rootRef = useRef<HTMLDivElement>(null);
  const onScreen = useOnScreen(rootRef);
  const shells = useLiveShells();
  const { sessions } = useSessionsList();
  const now = useRelativeNow();
  const shellIds = useMemo(() => shells.map((s) => s.shellId).join(","), [shells]);
  const activity = useShellActivity(onScreen, shellIds);
  // The busy shell whose End is waiting on the dialog.
  const [confirming, setConfirming] = useState<{ shell: ShellInfo; activity: ShellActivity }>();

  const names = useMemo(() => new Map(sessions.map((s) => [s.id, s.name])), [sessions]);
  // Only shells still alive: the cache can hold a shell that has just ended.
  const idleCount = shells.filter((s) => activity.get(s.shellId)?.busy === false).length;

  // Asks fresh rather than trusting the up-to-5s-old poll: this is the moment
  // that decides whether something gets killed. A host that can't say (gone)
  // ends it, as the dock's × does.
  const end = async (shell: ShellInfo) => {
    const [fresh] = await host.inspect(shell.shellId).catch(() => [] as ShellActivity[]);
    if (fresh?.busy) setConfirming({ shell, activity: fresh });
    else await host.end(shell.shellId).catch(() => undefined);
  };

  const endAllIdle = async () => {
    const all = await host.inspect().catch(() => [] as ShellActivity[]);
    const live = new Set(shells.map((s) => s.shellId));
    await Promise.all(
      all
        .filter((a) => !a.busy && live.has(a.shellId))
        .map((a) => host.end(a.shellId).catch(() => undefined)),
    );
  };

  return (
    <div ref={rootRef} className={styles.root}>
      {shells.length === 0 ? (
        <p className={styles.empty}>No shells are running.</p>
      ) : (
        <>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Session</th>
                <th>Folder</th>
                <th>Started</th>
                <th>Dock</th>
                <th>Running</th>
                <th>Memory</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {shells.map((shell) => (
                <ShellRow
                  key={shell.shellId}
                  shell={shell}
                  sessionName={names.get(shell.sessionId)}
                  activity={activity.get(shell.shellId)}
                  now={now}
                  onEnd={(s) => void end(s)}
                />
              ))}
            </tbody>
          </table>
          <div className={styles.actions}>
            <Button disabled={idleCount === 0} onClick={() => void endAllIdle()}>
              End all idle
            </Button>
          </div>
        </>
      )}
      <EndShellDialog
        activity={confirming?.activity}
        cancelLabel="Keep running"
        onConfirm={() => {
          const id = confirming?.shell.shellId;
          setConfirming(undefined);
          if (id) void host.end(id).catch(() => undefined);
        }}
        onCancel={() => setConfirming(undefined)}
        onDismiss={() => setConfirming(undefined)}
      />
    </div>
  );
}
