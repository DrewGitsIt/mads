import { describe, expect, it } from "vitest";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";

// Every type any subscribeToEvents caller subscribes to must be a declared
// contracts.EventType. The server refuses an undeclared type in ?types=, which
// fails the whole shared stream for every view in the window (and with the
// probe gone, the head read still succeeds, so the banner can't say why).
// sessionEventTypes.test.ts covers the shared session lists; this reads every
// call site's `types:` argument — an inline array or a SCREAMING_CASE
// constant, in the same file or imported, spreads included — and fails on any
// call it can't resolve, so a new call shape has to be taught here.

const SRC = "src";

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) out.push(...sourceFiles(path));
    else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith(".d.ts")) out.push(path);
  }
  return out;
}

// The text of the bracketed array starting at text[open] === "[".
function arrayAt(text: string, open: number): string {
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    if (text[i] === "[") depth++;
    else if (text[i] === "]" && --depth === 0) return text.slice(open, i + 1);
  }
  throw new Error("unterminated array");
}

function resolveImport(file: string, spec: string): string | undefined {
  if (!spec.startsWith(".")) return undefined;
  const base = join(dirname(file), spec);
  for (const candidate of [`${base}.ts`, `${base}.tsx`, join(base, "index.ts")]) {
    if (existsSync(candidate)) return candidate;
  }
  return undefined;
}

// The string literals a constant named `name` (visible in `file`) holds.
function resolveConstant(file: string, name: string, seen = new Set<string>()): string[] {
  const key = `${file}#${name}`;
  if (seen.has(key)) throw new Error(`cycle resolving ${key}`);
  seen.add(key);
  const text = readFileSync(file, "utf8");
  const def = new RegExp(`const ${name}\\b[^=]*=\\s*\\[`).exec(text);
  if (def) return literals(file, arrayAt(text, def.index + def[0].length - 1), seen);
  for (const m of text.matchAll(/import\s*(?:type\s*)?\{([^}]*)\}\s*from\s*"([^"]+)"/g)) {
    const names = m[1].split(",").map((s) => s.trim().split(/\s+as\s+/));
    const hit = names.find((parts) => (parts[1] ?? parts[0]) === name);
    if (!hit) continue;
    const target = resolveImport(file, m[2]);
    if (!target) throw new Error(`${file}: can't resolve import of ${name} from ${m[2]}`);
    return resolveConstant(target, hit[0], seen);
  }
  throw new Error(`${file}: can't find the definition of ${name}`);
}

function literals(file: string, array: string, seen: Set<string>): string[] {
  const out = [...array.matchAll(/"([^"]*)"/g)].map((m) => m[1]);
  for (const m of array.matchAll(/\.\.\.([A-Z][A-Z0-9_]*)/g)) out.push(...resolveConstant(file, m[1], seen));
  return out;
}

interface CallSite {
  where: string;
  types: string[];
}

function callSites(): CallSite[] {
  const sites: CallSite[] = [];
  for (const file of sourceFiles(SRC)) {
    if (file === join(SRC, "router", "events.ts")) continue;
    const text = readFileSync(file, "utf8");
    for (const call of text.matchAll(/subscribeToEvents\(\s*\{/g)) {
      const line = text.slice(0, call.index).split("\n").length;
      const where = `${relative(SRC, file)}:${line}`;
      const rest = text.slice(call.index + call[0].length);
      const arg = /^[^}]*?\btypes\s*:\s*/.exec(rest);
      if (!arg) throw new Error(`${where}: no types: in the subscribe params`);
      const after = rest.slice(arg[0].length);
      if (after.startsWith("[")) {
        sites.push({ where, types: literals(file, arrayAt(after, 0), new Set()) });
        continue;
      }
      const ident = /^([A-Z][A-Z0-9_]*)\b/.exec(after);
      if (!ident) throw new Error(`${where}: types: is neither an array literal nor a SCREAMING_CASE constant`);
      sites.push({ where, types: resolveConstant(file, ident[1]) });
    }
  }
  return sites;
}

describe("subscribed event types", () => {
  it("every subscribeToEvents caller subscribes only to types declared in pkg/contracts/event.go", () => {
    const go = readFileSync("../pkg/contracts/event.go", "utf8");
    const declared = new Set([...go.matchAll(/EventType = "([a-z_.]+)"/g)].map((m) => m[1]));
    expect(declared.size).toBeGreaterThan(20);
    const sites = callSites();
    // Sanity: the scan found the callers (there are ~40); a regex that
    // silently matched nothing would pass vacuously.
    expect(sites.length).toBeGreaterThan(30);
    for (const site of sites) {
      expect(site.types.length, site.where).toBeGreaterThan(0);
      for (const type of site.types) expect(declared, `${site.where}: ${type}`).toContain(type);
    }
  });
});
