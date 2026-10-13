import { newSessionCommand } from "./newSession";

/** /new [goal] — a new session with this one's folder, persona, model, trust and isolation. */
export const newCommand = newSessionCommand("new");
