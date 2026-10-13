import { newSessionCommand } from "./newSession";

/** /new-monitor [goal] — a new session that watches this one (monitor preset). */
export const newMonitorCommand = newSessionCommand("monitor");
