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
    return { parsed: { name: doc.name ?? "", stages: doc.stages ?? [] }, error: null };
  } catch (err) {
    return { parsed: null, error: err instanceof Error ? err.message : String(err) };
  }
}

/** ParsedTemplate -> YAML source — the inverse of parse, used by visual-mode edits. */
export function serialize(parsed: ParsedTemplate): string {
  return stringifyYaml(parsed);
}
