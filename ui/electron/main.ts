import { app, BrowserWindow, ipcMain, Menu, MenuItemConstructorOptions } from "electron";
import { join } from "node:path";
import { registerPtyHandlers } from "./pty/ptyHost";

const DEV_SERVER_URL = process.env.VITE_DEV_SERVER_URL;

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
      // file:// pages send Origin: null, which the Router's localhost-only
      // CORS policy rightly refuses. The window loads only our own bundled
      // renderer (no remote content), so relaxing webSecurity here is the
      // documented interim until the Router serves the UI same-origin (W7).
      webSecurity: false,
    },
  });

  registerPtyHandlers(ipcMain, win.webContents);

  ipcMain.handle("badge:set-count", (_event, count: number) => {
    app.setBadgeCount(count);
  });

  if (DEV_SERVER_URL) {
    win.loadURL(DEV_SERVER_URL);
  } else {
    win.loadFile(join(__dirname, "../dist/index.html"));
  }
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
