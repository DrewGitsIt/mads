import { resolve } from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      input: {
        main: resolve(__dirname, "index.html"),
        gallery: resolve(__dirname, "gallery.html"),
        conversation: resolve(__dirname, "conversation.html"),
        tasks: resolve(__dirname, "tasks.html"),
        files: resolve(__dirname, "files.html"),
        pipeline: resolve(__dirname, "pipeline.html"),
        dashboard: resolve(__dirname, "dashboard.html"),
      },
    },
  },
});
