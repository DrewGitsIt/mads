import type { Session } from "../../../router/types";

export type SessionKind = "work" | "retro-generated";
export type KindFilter = "all" | SessionKind;

export function classifySession(session: Session): SessionKind {
  return session.tags?.includes("retro") ? "retro-generated" : "work";
}

export function filterByKind(sessions: Session[], filter: KindFilter): Session[] {
  if (filter === "all") return sessions;
  return sessions.filter((session) => classifySession(session) === filter);
}
