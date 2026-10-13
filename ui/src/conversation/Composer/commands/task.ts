import type { CommandDescriptor } from "./types";

/**
 * /task ‹title› — creates an empty task bound to this session. Nothing goes
 * to the agent; a toast links to the new task.
 */
export const taskCommand: CommandDescriptor = {
  name: "task",
  description: "Create a task in this session",
  argHint: "<title>",
  argRequired: "/task needs a title",
  execute: async ({ args }, api) => {
    const { routerClient, sessionId } = api.ctx;
    try {
      const task = await routerClient.createTask({
        title: args.trim(),
        session_id: sessionId,
        source: "user",
      });
      const { openTask } = api;
      api.notify(
        `Task created: ${task.title}`,
        openTask
          ? { label: "Open task", onClick: () => openTask(task.id) }
          : undefined,
      );
    } catch (err) {
      api.fail(err instanceof Error ? err.message : String(err));
    }
  },
};
