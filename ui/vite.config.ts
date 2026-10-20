import { resolve } from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  // Electron loads dist/ via file:// — asset URLs must be relative or the
  // window renders blank (absolute /assets/ resolves to the filesystem root).
  base: "./",
  plugins: [react()],
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
});
