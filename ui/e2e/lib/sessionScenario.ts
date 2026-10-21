/**
 * The fake provider's script for chat sessions (services/session
 * -fake-scenario, appended to the Router's -session-cmd by topology.ts).
 * One file serves the whole suite, so every rule keys on a prompt only one
 * spec sends ("c5 …", chat-keyed.spec); prompts no rule matches fall through
 * to the fake's built-in dialect (`echo x` → "you said x", …), so every
 * other spec's sessions behave as before.
 *
 * The fake streams each block of `blocks` as its own keyed text item and
 * stored row, waits `delay_ms` before every chunk, runs `tools` between the
 * first block and the rest, and stores the user's row `user_lag_ms` after
 * the send (providers/fake/scenario.go).
 */

const quote = (s: string) => JSON.stringify(s);

interface SessionRule {
  match: string;
  blocks: string[][];
  delayMs: number;
  userLagMs?: number;
  tools?: { name: string; result: string; durationMs: number }[];
  hold?: boolean;
}

/** One text-join case (docs/CHAT-STREAM-DESIGN.md Test plan, R4A–E): two blocks, each streamed in chunks. */
export interface JoinCase {
  name: string;
  prompt: string;
  blocks: [string[], string[]];
  /** The joined reply's text with every whitespace run removed and markdown syntax dropped: what the bubble reads once whole. */
  flat: string;
}

export const JOIN_CASES: JoinCase[] = [
  {
    name: "R4A sentence then heading",
    prompt: "c5 join r4a",
    blocks: [["I'll check ", "the config files first."], ["## Config\n\n", "All good."]],
    flat: "I'llchecktheconfigfilesfirst.ConfigAllgood.",
  },
  {
    name: "R4B a list split across items",
    prompt: "c5 join r4b",
    blocks: [["Steps:\n\n", "- first\n", "- second\n"], ["- third\n", "- fourth\n\n", "Done."]],
    flat: "Steps:firstsecondthirdfourthDone.",
  },
  {
    name: "R4C two sentences",
    prompt: "c5 join r4c",
    blocks: [["One."], ["Two."]],
    flat: "One.Two.",
  },
  {
    name: "R4D sentence then list",
    prompt: "c5 join r4d",
    blocks: [["Here:"], ["- a\n", "- b"]],
    flat: "Here:ab",
  },
  {
    name: "R4E second item starts with a blank line",
    prompt: "c5 join r4e",
    blocks: [["Done.\n"], ["\nNext."]],
    flat: "Done.Next.",
  },
];

/** Words numbered prefix01..prefixNN, one chunk each: easy to check for gaps and repeats. */
export function numberedWords(prefix: string, n: number): string[] {
  return Array.from({ length: n }, (_, i) => `${prefix}${String(i + 1).padStart(2, "0")} `);
}

/** The user bubble outlives the store lag: one rule per variant, each slow enough to span the 2.5 s lag. */
export const LAG_PROMPTS = { start: "c5 lag start", visibility: "c5 lag visibility", reconnect: "c5 lag reconnect" };
export const LAG_WORDS = numberedWords("lagw", 14);

/** Text, tool, text: the first text settles while the tool runs. */
export const DUP_PROMPT = "c5 dup";
export const DUP_BLOCKS: [string[], string[]] = [["Checking ", "the tree."], ["Found ", "one file."]];
export const DUP_TOOL = "c5ls";

/** Two sessions streaming at once, for switching mid-item. */
export const SWITCH_PROMPTS = { a: "c5 switch a", b: "c5 switch b" };
export const SWITCH_WORDS = { a: numberedWords("swa", 28), b: numberedWords("swb", 28) };

/** goose's store lag: the pre-tool text and the call are stored only once the tool is done. */
export const HELD_PROMPT = "c5 held";
export const HELD_BLOCKS: [string[], string[]] = [["Reading ", "the notes."], ["Notes ", "look fine."]];
export const HELD_TOOL = "c5probe";

/** A reply long enough to reconnect in the middle of. */
export const CURSOR_PROMPT = "c5 cursor";
export const CURSOR_WORDS = numberedWords("cur", 30);

const RULES: SessionRule[] = [
  // Delays long enough that each block is visibly live before the next.
  ...JOIN_CASES.map((c) => ({ match: c.prompt, blocks: c.blocks, delayMs: 300 })),
  ...Object.values(LAG_PROMPTS).map((match) => ({ match, blocks: [LAG_WORDS], delayMs: 400, userLagMs: 2500 })),
  {
    match: DUP_PROMPT,
    blocks: DUP_BLOCKS,
    delayMs: 300,
    tools: [{ name: DUP_TOOL, result: "a.txt", durationMs: 4000 }],
  },
  { match: SWITCH_PROMPTS.a, blocks: [SWITCH_WORDS.a], delayMs: 250 },
  { match: SWITCH_PROMPTS.b, blocks: [SWITCH_WORDS.b], delayMs: 250 },
  {
    match: HELD_PROMPT,
    blocks: HELD_BLOCKS,
    delayMs: 200,
    tools: [{ name: HELD_TOOL, result: "probe ok", durationMs: 4000 }],
    hold: true,
  },
  { match: CURSOR_PROMPT, blocks: [CURSOR_WORDS], delayMs: 150 },
];

function ruleYAML(r: SessionRule): string[] {
  const out = [`  - match: ${quote(r.match)}`, "    blocks:"];
  for (const block of r.blocks) {
    out.push(`      - [${block.map(quote).join(", ")}]`);
  }
  out.push(`    delay_ms: ${r.delayMs}`);
  if (r.userLagMs) out.push(`    user_lag_ms: ${r.userLagMs}`);
  if (r.tools) {
    out.push("    tools:");
    for (const t of r.tools) {
      out.push(`      - name: ${quote(t.name)}`, `        result: ${quote(t.result)}`, `        duration_ms: ${t.durationMs}`);
    }
  }
  if (r.hold) out.push("    hold_until_tool_done: true");
  return out;
}

export const SESSION_SCENARIO = ["name: e2e-chat-sessions", "rules:", ...RULES.flatMap(ruleYAML), ""].join("\n");
