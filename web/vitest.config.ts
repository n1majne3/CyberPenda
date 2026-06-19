import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Separate Vitest config so the production `vite build` (which runs `tsc -b`
// via the build script) is unaffected. Test files live alongside source and
// import the same @/ alias.
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
    // Exclude node_modules and the built dist (which the daemon embeds).
    exclude: ["**/node_modules/**", "**/dist/**"],
    css: {
      // Process Tailwind/CSS so component styles are applied in jsdom.
      include: ["**/*.css"],
    },
  },
});
