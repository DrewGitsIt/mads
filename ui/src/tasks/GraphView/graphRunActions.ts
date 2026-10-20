import { createContext, useContext } from "react";

/**
 * Callbacks the run-mode node controls fire. Passed by context rather than
 * node data so a new callback identity from the caller doesn't rebuild every
 * xyflow node.
 */
export interface GraphRunActions {
  onToggleInclude?: (taskId: string) => void;
  onEditRetry?: (taskId: string) => void;
  onRetry?: (taskId: string) => void;
}

export const GraphRunActionsContext = createContext<GraphRunActions>({});

export function useGraphRunActions(): GraphRunActions {
  return useContext(GraphRunActionsContext);
}
