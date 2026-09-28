import type { Session } from "../../../router/types";

/**
 * The four fields the rail's own records already carry (mockup
 * docs/mockups/sessions-ux-v2.html §2b). A workspace's name is free text and a
 * workspace may hold several repos, so the workspace name is NOT derivable
 * from the directory — indexing both is what lets "mads" find a session whose
 * workspace is called "Main work" but whose cwd is ~/repo/mads.
 *
 * `dir` and `branch` are the query-facing names for `cwd` and
 * `worktree_branch`: they are what the user types, not what the record calls
 * them.
 */
export type SearchField = "name" | "workspace" | "dir" | "branch";

const SEARCH_FIELDS: SearchField[] = ["name", "workspace", "dir", "branch"];

/**
 * The one reason a card prints, most specific first. Two reasons on a 280px
 * card is an inventory; the question is only "why is this here".
 */
const REASON_PRIORITY: SearchField[] = ["workspace", "branch", "dir"];

/** Scope chips are only offered for these two — a branch or a name is not a scope. */
export const SCOPE_FIELDS: Extract<SearchField, "workspace" | "dir">[] = ["workspace", "dir"];

export interface SearchTerm {
  /** Set when the term carried a `field:` prefix — it then matches that field and nothing else. */
  field?: SearchField;
  /** Lowercased, quote-stripped, never empty. */
  needle: string;
}

const SCOPE_PREFIX = /^(workspace|dir|branch|name):([\s\S]*)$/;

/**
 * Whitespace-separated terms, except that a double-quoted run is one term —
 * a scope chip for a workspace called "Main work" has to commit as a single
 * token or it would parse as two unrelated terms.
 */
function tokenize(query: string): string[] {
  return query.match(/(?:[^\s"]|"[^"]*")+/g) ?? [];
}

/**
 * Terms are ANDed; each is a case-insensitive substring. No fuzzy matching:
 * over names as short as a rail card's, fuzzy produces confident nonsense.
 */
export function parseSearchQuery(query: string): SearchTerm[] {
  const terms: SearchTerm[] = [];
  for (const token of tokenize(query)) {
    const scoped = SCOPE_PREFIX.exec(token.toLowerCase());
    const needle = (scoped ? scoped[2] : token.toLowerCase()).replace(/"/g, "");
    // A bare "workspace:" is a half-typed token, not a filter that matches
    // everything — drop it so the list doesn't churn mid-type.
    if (!needle) continue;
    terms.push(scoped ? { field: scoped[1] as SearchField, needle } : { needle });
  }
  return terms;
}

/** The record field a query-facing field name reads. */
export function searchFieldValue(session: Session, field: SearchField): string {
  switch (field) {
    case "name":
      return session.name ?? "";
    case "workspace":
      return session.workspace ?? "";
    case "dir":
      return session.cwd ?? "";
    case "branch":
      return session.worktree_branch ?? "";
  }
}

export interface SearchMatch {
  /** Every field some term hit, deduped — what the reason line is chosen from. */
  where: SearchField[];
  /** 0 = name prefix, 1 = name substring, 2 = matched only away from the name. */
  rank: number;
}

/**
 * Whether a session answers the whole query, and how relevantly. Undefined
 * means at least one term hit none of the fields it was allowed to look at.
 */
export function matchSession(session: Session, terms: SearchTerm[]): SearchMatch | undefined {
  const where: SearchField[] = [];
  let rank = 3;
  const name = (session.name ?? "").toLowerCase();
  for (const term of terms) {
    const allowed = term.field ? [term.field] : SEARCH_FIELDS;
    const hit = allowed.filter((f) =>
      searchFieldValue(session, f).toLowerCase().includes(term.needle),
    );
    if (hit.length === 0) return undefined;
    for (const f of hit) if (!where.includes(f)) where.push(f);
    // A scoped term ranks on the field it was pinned to, never on the name it
    // was forbidden from looking at.
    const at = hit.includes("name") ? name.indexOf(term.needle) : -1;
    const termRank = at === 0 ? 0 : at > 0 ? 1 : 2;
    if (termRank < rank) rank = termRank;
  }
  return { where, rank };
}

export interface MatchReason {
  field: SearchField;
  value: string;
}

/**
 * The single line a card prints when it was not found by its name — without
 * it, typing "ui" and getting a session called "persona split" reads as a bug.
 * A card the name matched explains itself and prints nothing.
 */
export function matchReason(session: Session, match: SearchMatch): MatchReason | undefined {
  if (match.where.includes("name")) return undefined;
  const field = REASON_PRIORITY.find((f) => match.where.includes(f));
  return field ? { field, value: searchFieldValue(session, field) } : undefined;
}

export interface ScopeSuggestion {
  field: SearchField;
  value: string;
  /** The committable token, quoted when the value has whitespace. */
  token: string;
  /** How many sessions the token would select — the chip's trailing count. */
  count: number;
}

export const MAX_SCOPE_SUGGESTIONS = 3;

function scopeToken(field: SearchField, value: string): string {
  return `${field}:${/\s/.test(value) ? `"${value}"` : value}`;
}

/**
 * The rail's only extra search chrome: when the typed text names a workspace
 * or a directory, offer it as a committable token. There is no dropdown — in
 * a 280px rail it would cover the results it is suggesting — so this chip row
 * is the whole autosuggest surface and the list itself is the rest of it.
 */
export function scopeSuggestions(sessions: Session[], query: string): ScopeSuggestion[] {
  const raw = query.trim().toLowerCase();
  // Nothing typed, or a scope already committed — don't offer to scope a scope.
  if (!raw || raw.includes(":")) return [];
  const out: ScopeSuggestion[] = [];
  const seen = new Set<string>();
  for (const field of SCOPE_FIELDS) {
    for (const session of sessions) {
      const value = searchFieldValue(session, field);
      if (!value || !value.toLowerCase().includes(raw)) continue;
      const token = scopeToken(field, value);
      if (seen.has(token)) continue;
      seen.add(token);
      out.push({
        field,
        value,
        token,
        count: sessions.filter((s) => searchFieldValue(s, field) === value).length,
      });
    }
  }
  return out.slice(0, MAX_SCOPE_SUGGESTIONS);
}

/**
 * True for a focused element that is taking the user's typing — the guard
 * that keeps ⌘K from yanking focus out of the composer.
 */
export function isEditableElement(el: Element | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT";
}
