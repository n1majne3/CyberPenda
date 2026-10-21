#!/usr/bin/env node
import path from "node:path";
import { pathToFileURL } from "node:url";

const [repo, scratch, uiPort, apiPort] = process.argv.slice(2);
const web = path.join(repo, "web");
const { createServer } = await import(pathToFileURL(path.join(web, "node_modules/vite/dist/node/index.js")));
const target = `http://127.0.0.1:${apiPort}`;
const server = await createServer({
  root: web,
  configFile: path.join(web, "vite.config.ts"),
  configLoader: "bundle",
  envDir: false,
  cacheDir: path.join(scratch, "vite-cache"),
  server: {
    host: "127.0.0.1",
    port: Number(uiPort),
    strictPort: true,
    proxy: {
      "/api": { target, changeOrigin: false },
      "/health": { target, changeOrigin: false },
    },
  },
});
await server.listen();
server.printUrls();
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, async () => {
    await server.close();
    process.exit(0);
  });
}
