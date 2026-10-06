import { useSyncExternalStore } from "react";
import type { StagedAttachment, StagedAttachments } from "./stagedAttachments";

/** Subscribes a component to a staged-attachments list (see stagedAttachments.ts). */
export function useStagedAttachments(list: StagedAttachments): StagedAttachment[] {
  return useSyncExternalStore(list.subscribe, list.getSnapshot);
}
