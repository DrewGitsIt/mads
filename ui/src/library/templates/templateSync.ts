import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import type { ParsedTemplate } from "../../router/types";

export interface ParseResult {
  parsed: ParsedTemplate | null;
  error: string | null;
}

/** Client-side YAML -> ParsedTemplate. Never throws — parse errors surface as {error}. */
export function parse(source: string): ParseResult {
  try {
    const doc = (parseYaml(source) ?? {}) as Partial<ParsedTemplate>;
    // Visual-mode edits round-trip the FULL document through parse() ->
    // serialize() on every field change (TemplateEditor treats `source` as
    // the single source of truth and re-derives it after each edit). Any
    // top-level key we don't spread here — e.g. a template-level `defaults:`
    // block — would be silently deleted the moment the user touches an
    // unrelated field. Spread the whole doc first, then overlay the fields
    // we actually model, so unknown keys survive untouched.
    return { parsed: { ...doc, name: doc.name ?? "", stages: doc.stages ?? [] }, error: null };
  } catch (err) {
    return { parsed: null, error: err instanceof Error ? err.message : String(err) };
  }
}

/** ParsedTemplate -> YAML source — the inverse of parse, used by visual-mode edits. */
export function serialize(parsed: ParsedTemplate): string {
  return stringifyYaml(parsed);
}
