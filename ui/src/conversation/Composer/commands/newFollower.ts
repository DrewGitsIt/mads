import { newSessionCommand } from "./newSession";

/** /new-follower [goal] — a new session this one leads (leader preset; this session holds the link). */
export const newFollowerCommand = newSessionCommand("follower");
