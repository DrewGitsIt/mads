import { useCallback, useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import { contextOccupancy } from "../../router/modelDisplay";
import { formatTokens } from "../Dock/dockLabels";
import type { Usage } from "../../router/types";
import { hotkeyLabel } from "../../platform/hotkeys";
import styles from "./Composer.module.css";

export type ComposerMode = "chat" | "terminal";

export interface ComposerPlusMenuProps {
  expanded: boolean;
  disabled?: boolean;
  onAttach: () => void;
  onMention: () => void;
  onCommands: () => void;
  onToggleExpanded: () => void;
  /** View section — hidden when onModeChange is absent. */
  mode?: ComposerMode;
  onModeChange?: (mode: ComposerMode) => void;
  /** Context row — hidden without usage (never a made-up number). */
  usage?: Usage;
  contextWindow?: number;
  /** Compact now — hidden when absent. */
  onCompact?: () => void;
}

// Positioning only: the shared Popover opens downward; the dock's menus open
// upward from their opener (SESSION-DOCK-TASKS "Popovers").
const UPWARD = {
  top: "auto",
  bottom: "calc(100% + var(--space-2))",
  left: 0,
  width: 260,
  padding: 4,
  zIndex: "var(--z-dock-menu)",
} as const;


/**
 * The composer's `+` menu (mockup E): Input (attach, mention, commands,
 * expand/collapse, keyboard shortcuts), View (Chat | Terminal), and the
 * context row plus Compact now. The same button sits at the left of the input
 * row and of the expanded editor's action bar, so attach is reachable in both.
 */
export function ComposerPlusMenu({
  expanded,
  disabled,
  onAttach,
  onMention,
  onCommands,
  onToggleExpanded,
  mode = "chat",
  onModeChange,
  usage,
  contextWindow,
  onCompact,
}: ComposerPlusMenuProps) {
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<"main" | "keys">("main");
  const close = useCallback(() => setOpen(false), []);
  const occupancy = contextOccupancy(usage, contextWindow);

  function run(action: () => void) {
    setOpen(false);
    action();
  }

  const tone = occupancy
    ? occupancy.pct >= 90
      ? styles.toneBad
      : occupancy.pct >= 70
        ? styles.toneWarn
        : styles.toneDim
    : undefined;

  return (
    <Popover
      open={open}
      onClose={close}
      className={styles.menu}
      style={UPWARD}
      anchor={
        <button
          type="button"
          className={styles.plus}
          aria-label="Input options"
          aria-haspopup="menu"
          aria-expanded={open}
          title="Attach, mention, commands, view…"
          disabled={disabled}
          onClick={() => {
            setView("main");
            setOpen((o) => !o);
          }}
        >
          +
        </button>
      }
    >
      {view === "keys" ? (
        <div role="group" aria-label="Keyboard shortcuts">
          <button type="button" className={styles.menuHeadButton} onClick={() => setView("main")}>
            ‹ Keyboard
          </button>
          <div className={styles.keys}>
            <span>Send (queues while busy)</span>
            <kbd className={styles.kbd}>⏎</kbd>
            <span>New line</span>
            <kbd className={styles.kbd}>⇧⏎</kbd>
            <span>Expand editor</span>
            <kbd className={styles.kbd}>{hotkeyLabel("expandEditor")}</kbd>
            <span>Send from the editor</span>
            <kbd className={styles.kbd}>{hotkeyLabel("sendFromEditor")}</kbd>
            <span>Close the editor</span>
            <kbd className={styles.kbd}>{hotkeyLabel("collapseEditor")}</kbd>
            <span>Mention · Commands</span>
            <span>
              <kbd className={styles.kbd}>@</kbd> <kbd className={styles.kbd}>/</kbd>
            </span>
            <span>Attach files</span>
            <kbd className={styles.kbd}>{hotkeyLabel("attachFiles")}</kbd>
            <span>Chat ⇄ Terminal</span>
            <kbd className={styles.kbd}>{hotkeyLabel("toggleView")}</kbd>
          </div>
        </div>
      ) : (
        <div role="menu" aria-label="Input options">
          <div className={styles.menuHead}>Input</div>
          <button type="button" role="menuitem" className={styles.menuItem} onClick={() => run(onAttach)}>
            <span className={styles.menuGlyph}>📎</span>Attach files…
            <kbd className={styles.kbd}>{hotkeyLabel("attachFiles")}</kbd>
          </button>
          <button type="button" role="menuitem" className={styles.menuItem} onClick={() => run(onMention)}>
            <span className={styles.menuGlyph}>@</span>Mention file or session
            <kbd className={styles.kbd}>@</kbd>
          </button>
          <button type="button" role="menuitem" className={styles.menuItem} onClick={() => run(onCommands)}>
            <span className={styles.menuGlyph}>/</span>Commands
            <kbd className={styles.kbd}>/</kbd>
          </button>
          <button
            type="button"
            role="menuitem"
            className={styles.menuItem}
            onClick={() => run(onToggleExpanded)}
          >
            <span className={styles.menuGlyph}>{expanded ? "⤡" : "⤢"}</span>
            {expanded ? "Collapse editor" : "Expand editor"}
            <kbd className={styles.kbd}>{hotkeyLabel("expandEditor")}</kbd>
          </button>
          <button
            type="button"
            role="menuitem"
            className={styles.menuItem}
            onClick={() => setView("keys")}
          >
            <span className={styles.menuGlyph}>⌨</span>Keyboard shortcuts
          </button>

          {onModeChange && (
            <>
              <hr className={styles.menuSep} />
              <div className={styles.menuHead}>View</div>
              <div className={styles.seg2} role="group" aria-label="View">
                {(["chat", "terminal"] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    className={mode === m ? styles.segOn : undefined}
                    aria-pressed={mode === m}
                    onClick={() => run(() => onModeChange(m))}
                  >
                    {m === "chat" ? "💬 Chat" : "▮ Terminal"}
                  </button>
                ))}
              </div>
            </>
          )}

          {(occupancy || onCompact) && <hr className={styles.menuSep} />}
          {occupancy && (
            <div className={styles.ctxRow} data-testid="composer-context">
              Context · {formatTokens(occupancy.used)} / {formatTokens(occupancy.window)} ({occupancy.pct}%)
              <div className={styles.ctxBar}>
                <i className={tone} style={{ width: `${occupancy.pct}%` }} />
              </div>
            </div>
          )}
          {onCompact && (
            <button type="button" role="menuitem" className={styles.menuItem} onClick={() => run(onCompact)}>
              <span className={styles.menuGlyph}>⇣</span>Compact now
            </button>
          )}
        </div>
      )}
    </Popover>
  );
}
