import { resolve } from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { routerProxy } from "./vite-router-proxy";

export default defineConfig(({ command }) => ({
  // Electron loads dist/ via file:// — asset URLs must be relative or the
  // window renders blank (absolute /assets/ resolves to the filesystem root).
  base: "./",
  plugins: [react()],
  // `npm run dev`: the client's base URL is "" (the page calls its own
  // origin, and the dev server proxies the Router adding no credential —
  // vite-router-proxy.ts, H3) unless VITE_ROUTER_URL says otherwise. Inside
  // Electron (VITE_DEV_SERVER_URL), set VITE_ROUTER_URL to the Router: main
  // adds the bearer to the Router's origin only (H7).
  ...(command === "serve"
    ? {
        define: {
          "import.meta.env.VITE_ROUTER_URL": JSON.stringify(process.env.VITE_ROUTER_URL ?? ""),
        },
        server: { proxy: routerProxy() },
      }
    : {}),
  build: {
    rollupOptions: {
      input: {
        main: resolve(__dirname, "index.html"),
        gallery: resolve(__dirname, "gallery.html"),
        conversation: resolve(__dirname, "conversation.html"),
        tasks: resolve(__dirname, "tasks.html"),
        files: resolve(__dirname, "files.html"),
        library: resolve(__dirname, "library.html"),
        dashboard: resolve(__dirname, "dashboard.html"),
      },
    },
  },
}));
