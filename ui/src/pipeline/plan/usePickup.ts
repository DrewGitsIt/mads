import { useEffect, useState } from "react";
import type { PipelineClient } from "../client/pipelineClient";
import type { Task } from "../../router/types";
import type { PickupInfo } from "./runStatus";

/**
 * The session's Interrupted tasks and where a run of `templateId` picks each
 * up (GET /sessions/{id}/pipeline/pickup). Asked only when there is a
 * template and some confirmed task is in progress (nothing else can be
 * Interrupted), and again whenever the in-progress set or `refreshKey`
 * changes. Undefined until answered, and on an error: the views then read
 * in-progress tasks as they would without the answer.
 */
export function usePickup(
  client: PipelineClient,
  sessionId: string,
  templateId: string | undefined,
  tasks: Task[],
  refreshKey = "",
): PickupInfo | undefined {
  const [pickup, setPickup] = useState<PickupInfo>();
  const inProgress = tasks
    .filter((t) => t.status === "in_progress" && !t.draft)
    .map((t) => t.id)
    .sort()
    .join(",");

  useEffect(() => {
    if (!templateId || !inProgress) {
      setPickup(undefined);
      return;
    }
    let live = true;
    client
      .getPickup(sessionId, templateId)
      .then((answer) => {
        if (!live) return;
        setPickup({
          sharedDirectory: answer.shared_directory,
          tasks: Object.fromEntries(answer.tasks.map((t) => [t.task_id, t])),
        });
      })
      .catch(() => {
        if (live) setPickup(undefined);
      });
    return () => {
      live = false;
    };
  }, [client, sessionId, templateId, inProgress, refreshKey]);

  return pickup;
}
