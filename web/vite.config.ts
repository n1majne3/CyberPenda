import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Preserve the browser Host so the daemon can verify its Origin.
      "/api": { target: "http://127.0.0.1:8787", changeOrigin: false },
      "/health": { target: "http://127.0.0.1:8787", changeOrigin: false },
    },
  },
  build: {
    outDir: "dist",
  },
});
