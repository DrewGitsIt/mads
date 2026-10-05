import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import type { PtyBridge, PtySpawnOptions } from "./ptyBridge";
import { useElectronClipboardBridge, useElectronContextMenu } from "./ptyBridge";
import "@xterm/xterm/css/xterm.css";
import styles from "./TerminalView.module.css";

export interface TerminalViewProps {
  bridge: PtyBridge;
  spawnOptions?: PtySpawnOptions;
  className?: string;
  /**
   * Fires on ⌘E/Ctrl+E — captured via xterm's attachCustomKeyEventHandler
   * before xterm processes the keystroke, so no stray bytes reach the pty
   * when the compose overlay opens (ConversationTab renders TerminalComposer).
   */
  onComposeOpen?: () => void;
}

export interface TerminalViewHandle {
  /** Returns real DOM focus to xterm's own hidden input — used when the compose overlay closes. */
  focus(): void;
}

/**
 * xterm.js mounted over a PtyBridge. Used both for the Conversation tab's
 * terminal display mode (bound to the agent's own PTY session) and
 * ShellDock's docked general-purpose shell (bound to a separate pty) — each
 * caller supplies its own bridge/pty id (HANDOFF.md §4).
 */
export const TerminalView = forwardRef<TerminalViewHandle, TerminalViewProps>(
  function TerminalView({ bridge, spawnOptions, className, onComposeOpen }, ref) {
    const containerRef = useRef<HTMLDivElement>(null);
    const termRef = useRef<Terminal | null>(null);
    // Re-registering attachCustomKeyEventHandler on every onComposeOpen
    // identity change would mean detaching/reattaching mid-session — the
    // ref lets the mount-only effect below always call the latest callback.
    const onComposeOpenRef = useRef(onComposeOpen);
    onComposeOpenRef.current = onComposeOpen;

    // Both of these read `window.mads`, which preload sets up once before
    // the renderer ever mounts, so their identity is effectively static —
    // but route through refs anyway (same reasoning as onComposeOpenRef
    // above) so the mount-only effect below doesn't need either in its
    // dependency array.
    const contextMenuBridge = useElectronContextMenu();
    const contextMenuBridgeRef = useRef(contextMenuBridge);
    contextMenuBridgeRef.current = contextMenuBridge;

    const clipboardBridge = useElectronClipboardBridge();
    const clipboardBridgeRef = useRef(clipboardBridge);
    clipboardBridgeRef.current = clipboardBridge;

    useImperativeHandle(
      ref,
      () => ({
        focus: () => termRef.current?.focus(),
      }),
      [],
    );

    useEffect(() => {
      const container = containerRef.current;
      if (!container) return;

      const term = new Terminal({ convertEol: true });
      termRef.current = term;
      const fitAddon = new FitAddon();
      term.loadAddon(fitAddon);
      term.open(container);
      fitAddon.fit();

      /**
       * Copies the current selection out, preferring the Electron clipboard
       * bridge (routed through main — see ptyBridge.ts) and falling back to
       * navigator.clipboard outside Electron (vitest, a plain browser
       * preview). No-op when nothing is selected, so an accidental Ctrl+Shift+C
       * with no selection doesn't clobber the clipboard with "".
       */
      const copySelection = () => {
        const text = term.getSelection();
        if (!text) return;
        const electronClipboard = clipboardBridgeRef.current;
        if (electronClipboard) {
          void electronClipboard.writeText(text);
        } else {
          void navigator.clipboard?.writeText?.(text);
        }
      };

      /** Pastes clipboard text into the pty — same bridge/fallback split as copySelection. */
      const pasteClipboard = () => {
        const electronClipboard = clipboardBridgeRef.current;
        const read = electronClipboard
          ? electronClipboard.readText()
          : navigator.clipboard?.readText?.();
        void read?.then((text) => {
          if (text) term.paste(text);
        });
      };

      // Must run before xterm's own key handling — returning false swallows
      // the keystroke so no "e" (or the overlay's later Escape) ever reaches
      // the pty while toggling the compose overlay. Copy/paste piggybacks on
      // the same mechanism: xterm (rightly) treats a plain Ctrl+C as SIGINT
      // (sends ETX down the pty) and that must keep working unmodified, so
      // copy is bound to Ctrl+Shift+C instead — plus plain Cmd/Meta+C on
      // macOS, the platform convention, which has no SIGINT meaning here to
      // protect. Paste mirrors with V. Each chord also calls preventDefault:
      // the focused element is xterm's own hidden textarea, and without it
      // the browser's native copy/paste on that textarea would fire using
      // its own content/selection — which has nothing to do with what's
      // visually selected on the terminal's screen.
      term.attachCustomKeyEventHandler((e) => {
        if (e.type !== "keydown") return true;
        const key = e.key.toLowerCase();

        if ((e.metaKey || e.ctrlKey) && key === "e") {
          onComposeOpenRef.current?.();
          return false;
        }

        if ((e.ctrlKey && e.shiftKey && key === "c") || (e.metaKey && key === "c")) {
          e.preventDefault();
          copySelection();
          return false;
        }

        if ((e.ctrlKey && e.shiftKey && key === "v") || (e.metaKey && key === "v")) {
          e.preventDefault();
          pasteClipboard();
          return false;
        }

        return true;
      });

      // Right-click menu: only shown when the Electron bridge is present
      // (main pops the native Menu); elsewhere (vitest, a plain browser
      // preview) this is a no-op and whatever the platform/browser shows on
      // its own is left alone.
      const handleContextMenu = (e: MouseEvent) => {
        const menu = contextMenuBridgeRef.current;
        if (!menu) return;
        e.preventDefault();
        void menu.showTerminal({ hasSelection: term.hasSelection() }).then((action) => {
          switch (action) {
            case "copy":
              copySelection();
              break;
            case "paste":
              pasteClipboard();
              break;
            case "selectAll":
              term.selectAll();
              break;
            case "clear":
              term.clear();
              break;
          }
        });
      };
      container.addEventListener("contextmenu", handleContextMenu);

      let ptyId: string | undefined;
      let disposed = false;
      let unsubData: (() => void) | undefined;
      let unsubExit: (() => void) | undefined;

      const dataDisposable = term.onData((data) => {
        if (ptyId) bridge.write(ptyId, data);
      });

      const resizeObserver = new ResizeObserver(() => {
        fitAddon.fit();
        if (ptyId) bridge.resize(ptyId, term.cols, term.rows);
      });
      resizeObserver.observe(container);

      bridge
        .spawn({ ...spawnOptions, cols: term.cols, rows: term.rows })
        .then((id) => {
          if (disposed) {
            bridge.kill(id);
            return;
          }
          ptyId = id;
          unsubData = bridge.onData(id, (data) => term.write(data));
          unsubExit = bridge.onExit(id, () => {
            term.write("\r\n[process exited]\r\n");
          });
        })
        .catch((err: unknown) => {
          if (!disposed) {
            term.write(`\r\n[pty unavailable: ${String(err)}]\r\n`);
          }
        });

      return () => {
        disposed = true;
        container.removeEventListener("contextmenu", handleContextMenu);
        resizeObserver.disconnect();
        dataDisposable.dispose();
        unsubData?.();
        unsubExit?.();
        if (ptyId) bridge.kill(ptyId);
        termRef.current = null;
        term.dispose();
      };
      // A bridge identity change (e.g. ConversationTab reconnecting its
      // SessionEventSource) must re-run this effect so onData subscribes to
      // the *current* bridge — otherwise the terminal silently listens on a
      // stale, disconnected bridge forever (only a full remount used to fix
      // this). spawnOptions is still deliberately excluded: callers like
      // ShellDock pass a fresh object literal every render.
      // eslint-disable-next-line react-hooks/exhaustive-deps -- spawnOptions identity is intentionally excluded
    }, [bridge]);

    return (
      <div
        ref={containerRef}
        data-testid="terminal-view"
        className={[styles.terminal, className].filter(Boolean).join(" ")}
      />
    );
  },
);
