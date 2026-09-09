import { app, BrowserWindow, ipcMain, Menu, MenuItemConstructorOptions } from "electron";
import { join } from "node:path";
import { registerPtyHandlers } from "./pty/ptyHost";

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

  registerPtyHandlers(ipcMain, win.webContents);

  ipcMain.handle("badge:set-count", (_event, count: number) => {
    app.setBadgeCount(count);
  });

  // The Router serves the built UI same-origin (scripts/app.sh guarantees
  // it's already up before Electron launches), so this is always an http(s)
  // origin — never file://, which would send Origin: null and fail the
  // Router's localhost-only CORS policy.
  win.loadURL(DEV_SERVER_URL ?? ROUTER_BASE_URL);
}

app.whenReady().then(createWindow);

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
