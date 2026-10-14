import { useState } from "react";
import { useProviderInfos } from "../../router/useProviderInfos";
import { useProviderModels } from "../../router/useProviderModels";
import { hotkeyLabel } from "../../platform/hotkeys";
import { personaLabel } from "./dockLabels";
import { ModelSegment, PersonaSegment, TrustSegment, cx } from "./Pickers";
import type { SessionDockProps } from "./SessionDock";
import { WorkArea } from "./WorkArea";
import styles from "./StatusLine.module.css";

type OpenSegment = "model" | "trust" | "work";

export type StatusLineProps = Omit<SessionDockProps, "children" | "sheet" | "usage" | "contextWindow" | "onCompact">;

/**
 * The dock's bottom line (30px, bg0): persona, model ▾, trust ▾ on the
 * left; `⎇ workspace / branch → base` and the Commit/Land control on the
 * right. In terminal mode a `💬 Chat` segment leads (the way back) and a
 * `⌘E compose` segment sits before ⎇. One popover open at a time.
 */
export function StatusLine({
  session,
  mode,
  onModeChange,
  onComposeToggle,
  composeOpen,
  agents,
  workspaceName,
  checkoutBranch,
  onChangeModel,
  onChangeTrust,
  work,
  landOutcome,
}: StatusLineProps) {
  const [openSeg, setOpenSeg] = useState<OpenSegment | null>(null);
  const modelsByProvider = useProviderModels();
  const providerInfos = useProviderInfos();
  const close = () => setOpenSeg(null);
  const terminal = mode === "terminal";

  return (
    <div className={styles.status} data-testid="dock-status">
      {terminal && (
        <button
          type="button"
          className={cx(styles.seg, styles.viewBtn)}
          onClick={() => onModeChange("chat")}
          title={`Switch to Chat view (${hotkeyLabel("toggleView")})`}
          aria-label="Chat"
          data-segment="view"
        >
          <span aria-hidden="true">💬</span>
          <span className={cx(styles.text, styles.lblFull)}>Chat</span>
        </button>
      )}
      {session.agent && (
        <PersonaSegment label={personaLabel(session.agent, agents)} />
      )}
      {session.model && (
        <ModelSegment
          provider={session.provider}
          model={session.model}
          models={modelsByProvider[session.provider ?? ""] ?? []}
          open={openSeg === "model"}
          onOpen={() => setOpenSeg("model")}
          onClose={close}
          onSelect={(id) => {
            close();
            if (id !== session.model) onChangeModel?.(id);
          }}
        />
      )}
      <TrustSegment
        trust={session.trust}
        effectiveTrust={session.effective_trust}
        provider={session.provider}
        matrix={providerInfos.find((p) => p.name === session.provider)?.matrix}
        open={openSeg === "trust"}
        onOpen={() => setOpenSeg("trust")}
        onClose={close}
        onSelect={
          onChangeTrust &&
          ((t) => {
            close();
            if (t !== session.trust) onChangeTrust(t);
          })
        }
      />
      <span className={styles.spacer} />
      {terminal && onComposeToggle && (
        <button
          type="button"
          className={cx(styles.seg, styles.compose)}
          onClick={onComposeToggle}
          aria-pressed={composeOpen ?? undefined}
          aria-label={composeOpen ? "Close composer" : "Compose a long prompt"}
          title={`Compose a long prompt (${hotkeyLabel("expandEditor")})`}
          data-segment="compose"
        >
          <kbd className={cx(styles.kbd, styles.lblFull)}>{hotkeyLabel("expandEditor")}</kbd>
          <span className={styles.vMin} aria-hidden="true">
            ✎
          </span>
          <span className={cx(styles.text, styles.vFull)}>&nbsp;compose</span>
        </button>
      )}
      <WorkArea
        session={session}
        workspaceName={workspaceName}
        checkoutBranch={checkoutBranch}
        work={work}
        landOutcome={landOutcome}
        open={openSeg === "work"}
        onOpen={() => setOpenSeg("work")}
        onClose={close}
      />
    </div>
  );
}
