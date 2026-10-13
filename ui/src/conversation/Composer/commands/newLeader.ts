import { newSessionCommand } from "./newSession";

/** /new-leader [goal] — a new session that leads this one (leader preset; the new session holds the link). */
export const newLeaderCommand = newSessionCommand("leader");
