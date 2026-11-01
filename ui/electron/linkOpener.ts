import { spawn } from "node:child_process";
import { promises as fs } from "node:fs";
import os from "node:os";
import { basename, join } from "node:path";
import { BrowserWindow, dialog, ipcMain, shell, type MessageBoxOptions, type WebContents } from "electron";
import { isRunnable, resolveLinkTarget, splitCommand, type LinkTarget } from "./links";

/**
 * Opens links clicked in rendered markdown (agent replies, task text). The
 * renderer only hands over the href as written and the session's folder;
 * everything that decides what runs on this machine happens here, where page
 * content can't skip it:
 *   - web and localhost URLs go to the user's browser;
 *   - a file goes to Settings' "Open files with" command once the user has
 *     approved that exact command, or else to the OS default app — after a
 *     native confirmation when that app would run it — and to Finder / the
 *     file manager when no app takes it.
 * Not a Router route on purpose: the Router is unauthenticated and any
 * localhost page can call it, so it must never be the thing that opens files.
 */

/** What became of a click, for the renderer to show on the link. */
export type OpenOutcome = "opened" | "revealed" | "missing" | "none" | "cancelled" | "failed";
/** A link's kind for display; "missing" is a file link whose file isn't on this machine. */
export type LinkState = "web" | "localhost" | "file" | "missing" | "none";

const MAX_HREF = 4096;
const MAX_DESCRIBE = 200;
const CONFIG_TIMEOUT_MS = 2000;
const APPROVAL_FILE = "editor-command.json";

interface Deps {
  routerBaseUrl: string;
  userDataDir: string;
}

// One native prompt at a time: a page clicking a runnable link in a loop
// mustn't stack dialogs until one gets an absent-minded "Open".
let prompting = false;

export function registerLinkHandlers(deps: Deps): void {
  ipcMain.handle("links:describe", async (_event, hrefs: unknown, baseDir: unknown) => {
    if (!Array.isArray(hrefs)) return [];
    const base = typeof baseDir === "string" ? baseDir : undefined;
    return Promise.all(
      hrefs.slice(0, MAX_DESCRIBE).map((href) => (typeof href === "string" ? describe(href, base) : "none")),
    );
  });

  ipcMain.handle("links:open", async (event, href: unknown, baseDir: unknown): Promise<OpenOutcome> => {
    if (typeof href !== "string" || href.length > MAX_HREF) return "none";
    const base = typeof baseDir === "string" ? baseDir : undefined;
    const target = resolve(href, base);
    switch (target.kind) {
      case "web":
      case "localhost":
        await shell.openExternal(target.url);
        return "opened";
      case "none":
        return "none";
      case "file": {
        const path = await firstExisting(target.candidates);
        if (!path) return "missing";
        return openFile(event.sender, path, deps);
      }
    }
  });
}

function resolve(href: string, baseDir: string | undefined): LinkTarget {
  if (href.length > MAX_HREF) return { kind: "none" };
  return resolveLinkTarget(href, baseDir, os.homedir());
}

async function describe(href: string, baseDir: string | undefined): Promise<LinkState> {
  const target = resolve(href, baseDir);
  if (target.kind !== "file") return target.kind;
  return (await firstExisting(target.candidates)) ? "file" : "missing";
}

async function firstExisting(candidates: string[]): Promise<string | undefined> {
  for (const path of candidates) {
    try {
      await fs.stat(path);
      return path;
    } catch {
      // Try the next reading of the href.
    }
  }
  return undefined;
}

async function openFile(sender: WebContents, path: string, deps: Deps): Promise<OpenOutcome> {
  if (prompting) return "cancelled";
  prompting = true;
  try {
    const command = await editorCommand(deps.routerBaseUrl);
    const use = command ? await approveEditor(sender, command, deps.userDataDir) : "default";
    if (use === "cancel") return "cancelled";
    if (use === "command" && command) return await runEditor(sender, command, path);
    return await openWithDefaultApp(sender, path);
  } finally {
    prompting = false;
  }
}

/** Settings' "Open files with", read fresh so a change applies to the next click. */
async function editorCommand(routerBaseUrl: string): Promise<string | undefined> {
  try {
    const res = await fetch(`${routerBaseUrl}/config`, { signal: AbortSignal.timeout(CONFIG_TIMEOUT_MS) });
    if (!res.ok) return undefined;
    const config = (await res.json()) as { editor_command?: unknown };
    const command = typeof config.editor_command === "string" ? config.editor_command.trim() : "";
    return command || undefined;
  } catch {
    // Router down or slow: the default app still works.
    return undefined;
  }
}

/**
 * Whether to run `command`. The setting lives on the Router, which any
 * localhost page can write to, so a command is only ever run after the user
 * has seen that exact string here and said yes. The answer — yes or "use
 * the default app" — is remembered per command; a different string asks again.
 */
async function approveEditor(
  sender: WebContents,
  command: string,
  userDataDir: string,
): Promise<"command" | "default" | "cancel"> {
  const file = join(userDataDir, APPROVAL_FILE);
  let saved: { command?: string; approved?: boolean } = {};
  try {
    saved = JSON.parse(await fs.readFile(file, "utf8"));
  } catch {
    // First use, or an unreadable file: ask.
  }
  if (saved.command === command && typeof saved.approved === "boolean") {
    return saved.approved ? "command" : "default";
  }

  const choice = await ask(sender, {
    type: "question",
    message: "Open files with this command?",
    detail:
      `Settings › Editor › "Open files with" is set to:\n\n${command}\n\n` +
      "mads will run it with the file's path whenever you open a file link. " +
      "If you didn't set this, choose the default app instead and check Settings.",
    buttons: ["Use This Command", "Use the Default App", "Cancel"],
    defaultId: 0,
    cancelId: 2,
  });
  if (choice === 2) return "cancel";
  const approved = choice === 0;
  await fs.writeFile(file, JSON.stringify({ command, approved }), "utf8").catch(() => undefined);
  return approved ? "command" : "default";
}

async function runEditor(sender: WebContents, command: string, path: string): Promise<OpenOutcome> {
  const argv = splitCommand(command);
  if (!argv) {
    await showError(sender, "Couldn't read the editor command", `"${command}" has an unclosed quote. Fix it in Settings › Editor.`);
    return "failed";
  }
  const [file, ...args] = argv;
  // No shell: the path is its own argument, never parsed as a command line.
  const error = await new Promise<Error | undefined>((done) => {
    const child = spawn(file, [...args, path], { detached: true, stdio: "ignore" });
    child.once("spawn", () => {
      child.unref();
      done(undefined);
    });
    child.once("error", done);
  });
  if (!error) return "opened";
  const notFound = (error as NodeJS.ErrnoException).code === "ENOENT";
  await showError(
    sender,
    `Couldn't start ${basename(file)}`,
    notFound
      ? `"${file}" wasn't found. If it's on your shell's PATH, mads may not see that PATH when opened from the Dock or a launcher — use the full path in Settings › Editor.`
      : error.message,
  );
  return "failed";
}

async function openWithDefaultApp(sender: WebContents, path: string): Promise<OpenOutcome> {
  let realPath = path;
  let stat: { isFile: boolean; mode: number } = { isFile: false, mode: 0 };
  try {
    realPath = await fs.realpath(path);
    const s = await fs.stat(realPath);
    stat = { isFile: s.isFile(), mode: s.mode };
  } catch {
    return "missing";
  }

  if (isRunnable(realPath, stat, process.platform)) {
    const choice = await ask(sender, {
      type: "warning",
      message: `Open "${basename(path)}"?`,
      detail:
        "Opening this kind of file can run a program on your computer. " +
        "Only open it if you trust where it came from." +
        (realPath !== path ? `\n\nIt is a link to:\n${realPath}` : ""),
      buttons: ["Open", revealLabel(), "Cancel"],
      defaultId: 2,
      cancelId: 2,
    });
    if (choice === 2) return "cancelled";
    if (choice === 1) {
      shell.showItemInFolder(realPath);
      return "revealed";
    }
  }

  // "" on success; an error string when nothing could open it (an unknown
  // type, or no handler registered on Linux) — then show where it is instead.
  if ((await shell.openPath(path)) === "") return "opened";
  shell.showItemInFolder(path);
  return "revealed";
}

function revealLabel(): string {
  return process.platform === "darwin" ? "Show in Finder" : "Show in Folder";
}

async function ask(sender: WebContents, options: MessageBoxOptions): Promise<number> {
  const parent = BrowserWindow.fromWebContents(sender);
  const result = parent ? await dialog.showMessageBox(parent, options) : await dialog.showMessageBox(options);
  return result.response;
}

async function showError(sender: WebContents, message: string, detail: string): Promise<void> {
  await ask(sender, { type: "error", message, detail, buttons: ["OK"] });
}
