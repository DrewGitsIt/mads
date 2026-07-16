import { app, BrowserWindow, ipcMain } from "electron";
import { join } from "node:path";
import { registerPtyHandlers } from "./pty/ptyHost";

const DEV_SERVER_URL = process.env.VITE_DEV_SERVER_URL;

function createWindow(): void {
  const win = new BrowserWindow({
    width: 1280,
    height: 800,
    backgroundColor: "#1A1713",
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
