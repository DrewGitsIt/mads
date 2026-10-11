import { contextWindowForModel } from "../../router/modelDisplay";
import { pathBasename, sessionLocation } from "../../router/sessionLocation";
import type { Delegate, Session } from "../../router/types";
import { useProviderModels } from "../../router/useProviderModels";
import { ContextRim } from "./ContextRim";
import { shortBranch, shortPersona } from "./dockLabels";
import { Ladder, ModelSegment, cx } from "./Pickers";
import dock from "./SessionDock.module.css";
import line from "./StatusLine.module.css";
import area from "./WorkArea.module.css";

export interface DelegateDockProps {
  delegate: Delegate;
  /** The session that spawned it: its workspace, and its branch and window to fall back on. */
  parent?: Session;
  /** The parent's resolved workspace name (useWorkspaceForRef). */
  workspaceName?: string;
  /** The parent's checkout branch when it works in place (useCheckoutBranch). */
  checkoutBranch?: string;
}

const noop = () => {};

/**
 * A delegate's dock: the session dock's rim and status line, read-only and
 * holding only what is the delegate's own (docs/DELEGATES-DESIGN.md) — its
 * context, its type where a session shows its persona, the model its latest
 * call ran on, and where it works. No trust (it runs under its parent's), no
 * Commit/Land (the work is the parent's to land), no composer.
 */
export function DelegateDock({ delegate, parent, workspaceName, checkoutBranch }: DelegateDockProps) {
  const modelsByProvider = useProviderModels();
  const { type, model, usage } = delegate;

  // A call that didn't report its window: the parent's, when it runs the
  // same model (a scaled "[1m]" window is only ever reported, never in the
  // static table), else the static table's.
  const parentUsage = parent?.last_usage;
  const window =
    model && parentUsage?.context_window && baseModel(parentUsage.model ?? parent?.model) === baseModel(model)
      ? parentUsage.context_window
      : contextWindowForModel(model);

  const worktree = delegate.worktree_path;
  const where = parent ? sessionLocation(parent, workspaceName).name : workspaceName;
  const branch =
    delegate.branch ||
    (worktree ? pathBasename(worktree) : parent?.worktree_branch || checkoutBranch);
  const locText = [where, branch].filter(Boolean).join(" / ");

  return (
    <div className={dock.dock} data-testid="delegate-dock">
      <ContextRim usage={usage} contextWindow={window} />
      <div className={line.status} data-testid="dock-status">
        {type && (
          <span
            className={cx(line.seg, line.segStatic)}
            title={`Subagent type: ${type}`}
            aria-label={type}
            data-segment="persona"
          >
            <span className={line.avatar} aria-hidden="true">
              {type[0].toUpperCase()}
            </span>
            <Ladder full={type} short={shortPersona(type)} iconOnly />
          </span>
        )}
        {model && (
          <ModelSegment
            provider={parent?.provider}
            model={model}
            models={modelsByProvider[parent?.provider ?? ""] ?? []}
            open={false}
            onOpen={noop}
            onClose={noop}
          />
        )}
        <span className={line.spacer} />
        {locText && (
          <span
            className={cx(line.seg, line.segStatic, area.loc)}
            title={worktree ? `Worktree: ${worktree}` : undefined}
            aria-label={locText}
            data-testid="dock-location"
          >
            <span aria-hidden="true">⎇</span>
            {where && <span className={cx(line.text, line.vFull, area.where)}>{where} /</span>}
            {branch && (
              <>
                <span className={cx(area.branch, line.vFull)}>{branch}</span>
                <span className={cx(area.branch, line.vMid)}>{shortBranch(branch)}</span>
              </>
            )}
          </span>
        )}
      </div>
    </div>
  );
}

/** A model id without a scaled window's suffix: "claude-opus-5[1m]" → "claude-opus-5". */
function baseModel(id: string | undefined): string | undefined {
  return id?.replace(/\[[^\]]*\]$/, "");
}
