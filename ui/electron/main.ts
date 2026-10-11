import {
  app,
  BrowserWindow,
  clipboard,
  dialog,
  ipcMain,
  Menu,
  MenuItemConstructorOptions,
  MessageChannelMain,
  shell,
  utilityProcess,
  type UtilityProcess,
  type WebContents,
} from "electron";
import { join } from "node:path";
import { classifyNavigation } from "./externalLinks";
import { ShellRegistryCache } from "./pty/shellRegistryCache";
import type { ShellActivity, ShellInfo } from "./pty/shellHostCore";
import { HostRequests } from "./pty/hostRequests";
import { describeBusyShells, INSPECT_TIMEOUT_MS, QuitGuard } from "./pty/quitGuard";

const DEV_SERVER_URL = process.env.VITE_DEV_SERVER_URL;
// Same env var the renderer's own RouterClient reads (see ui/src/router/client.ts)
// so a single override points both processes at a non-default Router.
const ROUTER_BASE_URL = process.env.VITE_ROUTER_URL ?? "http://localhost:17373";

// Trimmed replacement for Electron's stock File/Edit/View/Window/Help menu,
// which is out of theme and mostly dead weight for this app. We still need a
// real Menu template (not Menu.setApplicationMenu(null)) because the edit
// roles below are what wire up Cmd/Ctrl+C/V/X/A/Z in text inputs, and macOS
// keeps a menu bar by platform convention.
const menuTemplate: MenuItemConstructorOptions[] = [
  ...(process.platform === "darwin"
    ? [{ role: "appMenu" } as MenuItemConstructorOptions]
    : []),
  {
    label: "File",
    submenu: [{ role: "quit" }],
  },
  {
    label: "Edit",
    submenu: [
      { role: "undo" },
      { type: "separator" },
      { role: "cut" },
      { role: "copy" },
      { role: "paste" },
      { role: "selectAll" },
    ],
  },
  {
    label: "View",
    submenu: [{ role: "reload" }, { role: "toggleDevTools" }],
  },
];

Menu.setApplicationMenu(Menu.buildFromTemplate(menuTemplate));

// App-wide, registered once: ipcMain.handle throws on a second registration
// for the same channel, so anything here must not live in createWindow
// (macOS re-runs it from "activate" after the last window closes).
function registerAppHandlers(): void {
  // Native right-click menu for the docked/terminal shell (TerminalView).
  // Resolves the clicked item, or null if the menu was dismissed without one
  // — the renderer performs the actual copy/paste/select/clear since only it
  // holds the xterm Terminal instance and the text under selection.
  ipcMain.handle(
    "contextMenu:terminal",
    (event, state: { hasSelection: boolean }) =>
      new Promise<"copy" | "paste" | "selectAll" | "clear" | null>((resolve) => {
        // Menu.popup's callback always fires once the menu closes, including
        // right after a successful item click — guard against resolving twice.
        let settled = false;
        const resolveOnce = (action: "copy" | "paste" | "selectAll" | "clear" | null) => {
          if (settled) return;
          settled = true;
          resolve(action);
        };
        const template: MenuItemConstructorOptions[] = [
          { label: "Copy", enabled: state.hasSelection, click: () => resolveOnce("copy") },
          { label: "Paste", click: () => resolveOnce("paste") },
          { type: "separator" },
          { label: "Select All", click: () => resolveOnce("selectAll") },
          { label: "Clear", click: () => resolveOnce("clear") },
        ];
        const parent = BrowserWindow.fromWebContents(event.sender);
        Menu.buildFromTemplate(template).popup({
          window: parent ?? undefined,
          callback: () => resolveOnce(null),
        });
      }),
  );

  // Clipboard access for the terminal's copy/paste, routed through main's
  // (unsandboxed) `clipboard` module — see preload.ts's `clipboard` namespace
  // for why this isn't just called directly from the preload script.
  ipcMain.handle("clipboard:read-text", () => clipboard.readText());
  ipcMain.handle("clipboard:write-text", (_event, text: string) => {
    clipboard.writeText(text);
  });

  ipcMain.handle("badge:set-count", (_event, count: number) => {
    app.setBadgeCount(count);
  });

  // Native folder chooser for every "where on disk" field (PathField). The
  // renderer can't get an absolute path any other way: a browser directory
  // input only ever exposes relative names.
  ipcMain.handle(
    "dialog:pick-directory",
    async (event, options?: { title?: string; defaultPath?: string }) => {
      const parent = BrowserWindow.fromWebContents(event.sender);
      const dialogOptions: Electron.OpenDialogOptions = {
        title: options?.title,
        defaultPath: options?.defaultPath,
        properties: ["openDirectory", "createDirectory"],
      };
      const result = parent
        ? await dialog.showOpenDialog(parent, dialogOptions)
        : await dialog.showOpenDialog(dialogOptions);
      return result.canceled ? null : (result.filePaths[0] ?? null);
    },
  );

  registerShellBroker();
}

// The shell host (docs/SHELL-DOCK-DESIGN.md D1) is a utilityProcess that owns
// every dock shell and node-pty with it. Main only brokers: it forks the host
// on first need and hands each window's preload one end of a MessageChannel
// whose other end goes to the host, so shell traffic never passes through
// main. A window asks for its port ("shell:connect") the first time its
// preload uses window.mads.shell — after a reload that is a fresh preload, so
// the reloaded page simply connects again.
let shellHost: UtilityProcess | undefined;
// Windows holding a port to the current host, by webContents id.
const shellClients = new Map<number, WebContents>();
// webContents whose lifecycle we already watch (hook once, not per connect).
const watchedForShells = new WeakSet<WebContents>();

// Which shells exist, as last reported by the host. Windows read it from here
// (shell:list) and hear changes as shell:changed, so asking never forks the
// host: no host means no shells.
const shellRegistry = new ShellRegistryCache((shells: ShellInfo[]) => {
  for (const win of BrowserWindow.getAllWindows()) {
    if (!win.webContents.isDestroyed()) win.webContents.send("shell:changed", shells);
  }
});

// How long quit waits for the host to hang up its shells before killing it anyway.
const END_ALL_TIMEOUT_MS = 1000;

// Quit-time requests to the host (what is running; end every shell), answered
// over the parent port. Created with the host; see hostRequests.ts.
let hostRequests: HostRequests | undefined;

/** Hangs up every shell and its jobs, waiting briefly for the host's ack, then stops the host. */
async function stopShellHost(): Promise<void> {
  if (!shellHost) return;
  await hostRequests?.request("end-all", END_ALL_TIMEOUT_MS);
  shellHost?.kill();
}

// D7: quitting with something running in a shell asks first (quitGuard.ts).
const quitGuard = new QuitGuard({
  hostRunning: () => shellHost !== undefined,
  inspect: async () => (await hostRequests?.request<ShellActivity[]>("inspect", INSPECT_TIMEOUT_MS)) ?? [],
  confirm: async (busy) => {
    const win = BrowserWindow.getAllWindows()[0];
    const options: Electron.MessageBoxOptions = {
      type: "warning",
      buttons: ["Quit and end them", "Cancel"],
      defaultId: 1,
      cancelId: 1,
      message:
        busy.length === 1
          ? "A shell still has something running"
          : `${busy.length} shells still have something running`,
      detail: `${describeBusyShells(busy)}\n\nQuitting ends these shells and stops what they are running.`,
    };
    const result = win ? await dialog.showMessageBox(win, options) : await dialog.showMessageBox(options);
    return result.response === 0;
  },
  killHost: stopShellHost,
  quit: () => app.quit(),
});

function ensureShellHost(): UtilityProcess {
  if (shellHost) return shellHost;
  const host = utilityProcess.fork(join(__dirname, "pty", "shellHost.js"), [], {
    serviceName: "mads-shell-host",
  });
  shellHost = host;
  hostRequests = new HostRequests((message) => host.postMessage(message));
  host.on(
    "message",
    (message: { type?: string; shells?: ShellInfo[] | ShellActivity[]; requestId?: number }) => {
      if (shellHost !== host) return;
      if (message.type === "shells-changed" && message.shells) {
        shellRegistry.set(message.shells as ShellInfo[]);
      } else if (message.type === "inspect-result" && message.requestId !== undefined) {
        hostRequests?.reply(message.requestId, message.shells ?? []);
      } else if (message.type === "end-all-done" && message.requestId !== undefined) {
        hostRequests?.reply(message.requestId, true);
      }
    },
  );
  host.on("exit", () => {
    if (shellHost !== host) return;
    // Nobody is going to answer a pending quit-time request now.
    hostRequests?.failAll();
    shellRegistry.clear();
    // Its shells died with it. Tell every window that held a port so the dock
    // can say so; the next shell:connect forks a fresh host.
    shellHost = undefined;
    for (const wc of shellClients.values()) {
      if (!wc.isDestroyed()) wc.send("shell:host-exited");
    }
    shellClients.clear();
  });
  return host;
}

/** Tells the host this window's view is gone: its shells detach but keep running. */
function releaseShellWindow(windowId: number): void {
  if (!shellClients.delete(windowId)) return;
  shellHost?.postMessage({ type: "window-gone", windowId });
}

function watchShellWindow(wc: WebContents): void {
  if (watchedForShells.has(wc)) return;
  watchedForShells.add(wc);
  const windowId = wc.id; // not readable once destroyed
  // A main-frame navigation (including reload) unloads the page that was
  // attached; same-document ones (hash/pushState) keep it.
  wc.on("did-start-navigation", (details) => {
    if (details.isMainFrame && !details.isSameDocument) releaseShellWindow(windowId);
  });
  wc.on("render-process-gone", () => releaseShellWindow(windowId));
  wc.on("destroyed", () => releaseShellWindow(windowId));
}

function registerShellBroker(): void {
  ipcMain.handle("shell:list", () => shellRegistry.get());

  ipcMain.handle("shell:connect", (event) => {
    const wc = event.sender;
    const host = ensureShellHost();
    watchShellWindow(wc);
    const { port1, port2 } = new MessageChannelMain();
    shellClients.set(wc.id, wc);
    host.postMessage({ type: "connect", windowId: wc.id }, [port1]);
    wc.postMessage("shell:port", null, [port2]);
  });

  // Don't leave the host (and its shells) to the OS's mercy at quit — but ask
  // first when a shell is busy.
  app.on("before-quit", (event) => quitGuard.handleBeforeQuit(event));
}

function createWindow(): void {
  const win = new BrowserWindow({
    width: 1280,
    height: 800,
    backgroundColor: "#1A1713",
    // The menu bar itself is chrome we don't want visible; Alt still reveals
    // it as an escape hatch. macOS has no equivalent bar to hide.
    autoHideMenuBar: process.platform !== "darwin",
    webPreferences: {
      preload: join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });

  // Links that open a new window (target="_blank" — e.g. preflight's
  // vendor install guides) belong in the user's browser, never in a child
  // BrowserWindow of the app; anything that isn't plain http(s) goes
  // nowhere.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//.test(url)) void shell.openExternal(url);
    return { action: "deny" };
  });

  // A plain <a href> (agent markdown strips target) navigates this window
  // itself, and there is no back button. Off-origin http(s) opens in the
  // user's browser instead; the window never leaves the app. will-redirect
  // covers a same-origin URL that bounces elsewhere.
  const appUrl = DEV_SERVER_URL ?? ROUTER_BASE_URL;
  const keepInBrowser = (event: { preventDefault(): void }, url: string) => {
    const verdict = classifyNavigation(url, appUrl);
    if (verdict === "allow") return;
    event.preventDefault();
    if (verdict === "external") void shell.openExternal(url);
  };
  win.webContents.on("will-navigate", (event, url) => keepInBrowser(event, url));
  win.webContents.on("will-redirect", (event, url) => keepInBrowser(event, url));

  // The Router serves the built UI same-origin (scripts/app.sh guarantees
  // it's already up before Electron launches), so this is always an http(s)
  // origin — never file://, which would send Origin: null and fail the
  // Router's localhost-only CORS policy.
  win.loadURL(appUrl);
}

app.whenReady().then(() => {
  registerAppHandlers();
  createWindow();
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") {
    app.quit();
  }
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    createWindow();
  }
});
