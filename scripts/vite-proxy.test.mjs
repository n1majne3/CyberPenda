import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer as createHTTPServer } from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";
import { fileURLToPath } from "node:url";
import { createServer, loadConfigFromFile } from "../web/node_modules/vite/dist/node/index.js";

test("Vite preserves browser origin and host for operator session bootstrap", async () => {
  let received;
  const backend = createHTTPServer((req, res) => {
    received = req.headers;
    res.writeHead(204).end();
  });
  backend.listen(0, "127.0.0.1");
  await once(backend, "listening");
  const cacheDir = await mkdtemp(join(tmpdir(), "cyberpenda-vite-proxy-"));
  let frontend;
  try {
    const root = fileURLToPath(new URL("../web/", import.meta.url));
    const { config } = await loadConfigFromFile({ command: "serve", mode: "development" }, `${root}vite.config.ts`);
    const rule = config.server.proxy["/api"];
    const target = `http://127.0.0.1:${backend.address().port}`;
    config.server.proxy["/api"] = typeof rule === "string" ? target : { ...rule, target };
    frontend = await createServer({ ...config, root, cacheDir, configFile: false, optimizeDeps: { noDiscovery: true, include: [] }, server: { ...config.server, host: "127.0.0.1", port: 0 } });
    await frontend.listen();
    const origin = `http://127.0.0.1:${frontend.httpServer.address().port}`;
    const response = await fetch(`${origin}/api/operator-session`, { method: "POST", headers: { Origin: origin, "Sec-Fetch-Site": "same-origin" } });
    assert.equal(response.status, 204);
    assert.equal(received.host, new URL(origin).host);
    assert.equal(received.origin, origin);
    assert.equal(received["sec-fetch-site"], "same-origin");
  } finally {
    await frontend?.close();
    await rm(cacheDir, { recursive: true, force: true });
    backend.closeAllConnections();
    await new Promise((resolve) => backend.close(resolve));
  }
});
