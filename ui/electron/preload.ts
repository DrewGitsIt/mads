import { contextBridge } from "electron";

// Empty contextBridge stub — real IPC surface is a later wave once Router
// contracts exist.
contextBridge.exposeInMainWorld("mads", {});
