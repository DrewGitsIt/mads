import { delimiter } from "node:path";

/**
 * Strips the environment variables npm injects into its child processes
 * before handing env to a spawned shell. scripts/app.sh launches Electron via
 * `npm --prefix ui start`, so the main process — and everything it spawns,
 * including this docked shell — inherits npm's lifecycle env wholesale:
 * ~20 `npm_*` vars, `INIT_CWD`, `NODE` (npm sets this to its own Node binary
 * path; nothing else does in this context), and a PATH with npm-prepended
 * `node_modules/.bin` and `node-gyp-bin` entries from every ancestor
 * directory. None of that belongs in a shell the user opens to do their own
 * work — at best it's noise, at worst it actively breaks things (nvm refuses
 * to manage the shell's Node version while `npm_config_prefix` is set, print-
 * ing "nvm is not compatible with the npm_config_prefix environment variable"
 * on every new shell).
 *
 * Pure and side-effect free so it's testable without node-pty or Electron.
 */
export function shellEnv(env: NodeJS.ProcessEnv): Record<string, string> {
  const result: Record<string, string> = {};
  for (const [key, value] of Object.entries(env)) {
    if (value === undefined) continue;
    if (/^npm_/i.test(key)) continue;
    if (key === "INIT_CWD" || key === "NODE") continue;
    result[key] = value;
  }

  if (result.PATH) {
    result.PATH = result.PATH
      .split(delimiter)
      .filter((entry) => !entry.endsWith("/node_modules/.bin") && !entry.includes("node-gyp-bin"))
      .join(delimiter);
  }

  // Lets shell prompts/tools (and the user) identify this as mads' docked
  // shell rather than a plain terminal — but only if nothing upstream (the
  // user's own environment) already claims TERM_PROGRAM.
  if (!result.TERM_PROGRAM) {
    result.TERM_PROGRAM = "mads";
  }

  return result;
}
