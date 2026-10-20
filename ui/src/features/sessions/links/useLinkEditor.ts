import { useCallback, useMemo, useRef, useState } from "react";
import type { RouterClient } from "../../../router/client";
import type { Link, LinkCadence, Session } from "../../../router/types";
import type { RailLinkEditor } from "../rail/SessionRail";
import {
  EMPTY_DRAFT,
  addFromRail,
  applyLinkDraft,
  effectiveEdges,
  firstDoubleLeader,
  firstLoop,
  linkBlockedReason,
  mergeFreshLinks,
  pairChanges,
  plan,
  removeEdge,
  setCadence,
  setRole,
  swapDirection,
  type EffectiveEdge,
  type LinkDraft,
  type LinkRole,
  type PairChange,
} from "./linkDraft";

/** The last save's failure, as the footer and the failed line show it. */
export interface SaveError {
  appliedCount: number;
  total: number;
  key: string;
  label: string;
  message: string;
  refused: boolean;
}

interface EditorState {
  focus: string;
  /** Breadcrumbs: every session re-centred on, oldest first; the last is the focus. */
  trail: string[];
  draft: LinkDraft;
  /** The pair whose edge editor is open. */
  pick?: string;
  confirm: boolean;
  saving: boolean;
  saveErr?: SaveError;
  /** Part of some save already landed, so discarding can't undo everything. */
  appliedSome: boolean;
  /**
   * Links fetched straight after a failed save, laid over the live feed until
   * the feed next changes (by then it has caught up), so a retry plans from
   * what the server holds now.
   */
  fresh?: { base: Link[]; sessionIds: string[]; links: Link[] };
}

export interface LinkEditor {
  isOpen: boolean;
  focus?: string;
  trail: string[];
  pick?: string;
  confirm: boolean;
  saving: boolean;
  saveErr?: SaveError;
  appliedSome: boolean;
  /** The server's links as the editor currently believes them. */
  serverLinks: Link[];
  edges: EffectiveEdge[];
  changes: PairChange[];
  loop: string[] | null;
  doubleLeader: { subject: string; holders: string[] } | null;
  nameOf: (id: string) => string;
  sessionOf: (id: string) => Session | undefined;
  /** Why a session can't be linked to the focus, or undefined. */
  blockedReason: (id: string) => string | undefined;
  openOn: (sessionId: string, returnFocusTo?: HTMLElement | null) => void;
  /** A rail card, chain or shortcut click while open. False when the session can't be linked (the rail shakes it). */
  railClick: (sessionId: string) => boolean;
  recenter: (sessionId: string) => void;
  pickEdge: (key: string | undefined) => void;
  setRole: (key: string, role: LinkRole) => void;
  setCadence: (key: string, cadence: LinkCadence) => void;
  swap: (key: string) => void;
  remove: (key: string) => void;
  save: () => Promise<void>;
  /** Esc, Cancel, × or a click on the blurred pane: asks first when anything is unsaved. */
  requestClose: () => void;
  keepEditing: () => void;
  discard: () => void;
}

/**
 * The link editor's state, owned by SessionsSurface so the rail (which adds
 * links while the editor is open) and the modal share one draft.
 */
export function useLinkEditor({
  links,
  sessions,
  client,
}: {
  links: Link[];
  sessions: Session[];
  client: RouterClient;
}): LinkEditor {
  const [state, setState] = useState<EditorState>();
  const returnFocus = useRef<HTMLElement | null>(null);
  const linksRef = useRef(links);
  linksRef.current = links;

  const sessionsById = useMemo(() => new Map(sessions.map((s) => [s.id, s])), [sessions]);
  const sessionOf = useCallback((id: string) => sessionsById.get(id), [sessionsById]);
  const nameOf = useCallback((id: string) => sessionsById.get(id)?.name || "Untitled session", [sessionsById]);

  const fresh = state?.fresh;
  const serverLinks = useMemo(
    () => (fresh && fresh.base === links ? mergeFreshLinks(links, fresh.sessionIds, fresh.links) : links),
    [links, fresh],
  );
  const draft = state?.draft ?? EMPTY_DRAFT;
  const edges = useMemo(() => effectiveEdges(serverLinks, draft), [serverLinks, draft]);
  const changes = useMemo(() => pairChanges(serverLinks, draft), [serverLinks, draft]);
  const loop = useMemo(() => firstLoop(edges), [edges]);
  const doubleLeader = useMemo(() => firstDoubleLeader(edges), [edges]);

  // Edits read the latest server links at call time, not the render's.
  const serverRef = useRef(serverLinks);
  serverRef.current = serverLinks;

  const close = useCallback(() => {
    setState(undefined);
    const target = returnFocus.current;
    returnFocus.current = null;
    // After the modal unmounts, or focus would land in a node about to vanish.
    if (target) setTimeout(() => target.isConnected && target.focus(), 0);
  }, []);

  const edit = useCallback((fn: (s: EditorState, server: Link[]) => Partial<EditorState>) => {
    setState((s) => (s && !s.confirm ? { ...s, ...fn(s, serverRef.current) } : s));
  }, []);

  const focus = state?.focus;
  const blockedReason = useCallback(
    (id: string) => (focus ? linkBlockedReason(sessionsById.get(focus), sessionsById.get(id)) : undefined),
    [focus, sessionsById],
  );

  const save = useCallback(async () => {
    const s = state;
    if (!s || s.saving) return;
    const server = serverRef.current;
    const ops = plan(server, s.draft);
    if (ops.length === 0) {
      close();
      return;
    }
    setState((cur) => cur && { ...cur, saving: true, pick: undefined });
    const result = await applyLinkDraft(client, ops);
    if (!result.failed) {
      close();
      return;
    }
    const { op, message, refused } = result.failed;
    const edge = op.kind === "create" ? op.edge : { granting: op.link.granting_session_id, subject: op.link.subject_session_id };
    setState((cur) =>
      cur && {
        ...cur,
        saving: false,
        appliedSome: cur.appliedSome || result.appliedCount > 0,
        // A refusal is about one line: open its editor, where the message is.
        pick: refused ? op.key : undefined,
        saveErr: {
          appliedCount: result.appliedCount,
          total: result.total,
          key: op.key,
          label: `${nameOf(edge.granting)} → ${nameOf(edge.subject)}`,
          message,
          refused,
        },
        fresh: result.fresh ? { base: linksRef.current, ...result.fresh } : cur.fresh,
      },
    );
  }, [state, client, close, nameOf]);

  const requestClose = useCallback(() => {
    if (!state || state.saving) return;
    if (pairChanges(serverRef.current, state.draft).length > 0) setState({ ...state, confirm: true, pick: undefined });
    else close();
  }, [state, close]);

  return {
    isOpen: state !== undefined,
    focus,
    trail: state?.trail ?? [],
    pick: state?.pick,
    confirm: state?.confirm ?? false,
    saving: state?.saving ?? false,
    saveErr: state?.saveErr,
    appliedSome: state?.appliedSome ?? false,
    serverLinks,
    edges,
    changes,
    loop,
    doubleLeader,
    nameOf,
    sessionOf,
    blockedReason,
    openOn: (sessionId, returnFocusTo) => {
      returnFocus.current = returnFocusTo ?? null;
      setState({ focus: sessionId, trail: [sessionId], draft: EMPTY_DRAFT, confirm: false, saving: false, appliedSome: false });
    },
    railClick: (sessionId) => {
      if (!state || state.confirm || state.saving) return true;
      if (sessionId === state.focus) return true;
      if (blockedReason(sessionId)) return false;
      edit((s, server) => {
        const r = addFromRail(server, s.draft, s.focus, sessionId);
        return { draft: r.draft, pick: r.key };
      });
      return true;
    },
    recenter: (sessionId) =>
      edit((s) => {
        if (sessionId === s.focus) return {};
        const at = s.trail.indexOf(sessionId);
        return { focus: sessionId, pick: undefined, trail: at >= 0 ? s.trail.slice(0, at + 1) : [...s.trail, sessionId] };
      }),
    pickEdge: (key) => edit(() => ({ pick: key })),
    setRole: (key, role) => edit((s, server) => ({ draft: setRole(server, s.draft, key, role), pick: key })),
    setCadence: (key, cadence) => edit((s, server) => ({ draft: setCadence(server, s.draft, key, cadence), pick: key })),
    swap: (key) => edit((s, server) => ({ draft: swapDirection(server, s.draft, key), pick: key })),
    remove: (key) => edit((s, server) => ({ draft: removeEdge(server, s.draft, key), pick: undefined })),
    save,
    requestClose,
    keepEditing: () => setState((s) => s && { ...s, confirm: false }),
    discard: close,
  };
}

/** The rail's view of an open editor: what to tag, check and dim, and where its clicks go. */
export function useRailLinkEditor(editor: LinkEditor): RailLinkEditor | undefined {
  const { isOpen, focus, edges, blockedReason, railClick } = editor;
  const railClickRef = useRef(railClick);
  railClickRef.current = railClick;
  return useMemo(() => {
    if (!isOpen || !focus) return undefined;
    const linkedIds = new Set<string>();
    for (const e of edges) {
      if (e.granting === focus) linkedIds.add(e.subject);
      else if (e.subject === focus) linkedIds.add(e.granting);
    }
    return { focusId: focus, linkedIds, blockedReason, onPick: (id) => railClickRef.current(id) };
  }, [isOpen, focus, edges, blockedReason]);
}
