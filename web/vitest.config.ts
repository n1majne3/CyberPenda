import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Separate Vitest config so the production `vite build` (which runs `tsc -b`
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    exclude: ["**/node_modules/**", "**/dist/**", "**/e2e/**"],
    css: {
      // Process Tailwind/CSS so component styles are applied in jsdom.
      include: ["**/*.css"],
    },
  },
});
