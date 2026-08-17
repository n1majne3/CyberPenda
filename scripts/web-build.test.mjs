import assert from "node:assert/strict";
import { link, mkdtemp, mkdir, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { spawn } from "node:child_process";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  ensureEmbedStub,
  ensureWebDependencies,
  publishEmbeddedUI,
  runWebBuildCommand,
  syncEmbeddedUI,
  webDependencyRepairReason,
} from "./web-build.mjs";

async function temporaryTree(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), "cyberpenda-web-build-"));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });
  const source = path.join(root, "web-dist");
  const destination = path.join(root, "embed-dist");
  await mkdir(source, { recursive: true });
  await mkdir(destination, { recursive: true });
  return { root, source, destination };
}

test("syncEmbeddedUI copies source contents and preserves the embed directory", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await writeFile(path.join(source, "index.html"), "current UI");
  await writeFile(path.join(destination, ".gitkeep"), "tracked placeholder");
  const destinationBefore = await stat(destination);

  await syncEmbeddedUI(source, destination);

  assert.equal(await readFile(path.join(destination, "index.html"), "utf8"), "current UI");
  assert.equal(await readFile(path.join(destination, ".gitkeep"), "utf8"), "tracked placeholder");
  assert.equal((await stat(destination)).ino, destinationBefore.ino);
});

test("syncEmbeddedUI removes stale generated entries but protects .gitkeep", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await mkdir(path.join(source, "assets"));
  await writeFile(path.join(source, "assets", "current.js"), "current");
  await mkdir(path.join(destination, "assets"));
  await writeFile(path.join(destination, "assets", "old.js"), "stale");
  await mkdir(path.join(destination, "obsolete"));
  await writeFile(path.join(destination, "obsolete", "chunk.js"), "stale");
  await writeFile(path.join(destination, ".gitkeep"), "root keep");
  await writeFile(path.join(destination, "obsolete", ".gitkeep"), "nested keep");

  await syncEmbeddedUI(source, destination);

  assert.deepEqual((await readdir(path.join(destination, "assets"))).sort(), ["current.js"]);
  assert.equal(await readFile(path.join(destination, "obsolete", ".gitkeep"), "utf8"), "nested keep");
  await assert.rejects(readFile(path.join(destination, "obsolete", "chunk.js")), { code: "ENOENT" });
});

test("syncEmbeddedUI handles nested output and file-directory transitions", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await writeFile(path.join(source, "assets"), "temporary file");
  await mkdir(path.join(destination, "assets"));
  await writeFile(path.join(destination, "assets", "old.js"), "old");
  await mkdir(path.join(source, "route"));
  await writeFile(path.join(source, "route", "index.html"), "nested");
  await writeFile(path.join(destination, "route"), "temporary file");

  await syncEmbeddedUI(source, destination);

  assert.equal(await readFile(path.join(destination, "assets"), "utf8"), "temporary file");
  assert.equal(await readFile(path.join(destination, "route", "index.html"), "utf8"), "nested");
});

test("syncEmbeddedUI validates the complete source before changing the destination", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await writeFile(path.join(destination, "current.js"), "must remain");
  await writeFile(path.join(source, "index.html"), "new");
  await (await import("node:fs/promises")).symlink(path.join(source, "index.html"), path.join(source, "linked.html"));

  await assert.rejects(syncEmbeddedUI(source, destination), /unsupported entry/);
  assert.equal(await readFile(path.join(destination, "current.js"), "utf8"), "must remain");
  await assert.rejects(readFile(path.join(destination, "index.html")), { code: "ENOENT" });
});

test("syncEmbeddedUI rejects a missing source before changing the destination", async (t) => {
  const { root, destination } = await temporaryTree(t);
  await writeFile(path.join(destination, "current.js"), "must remain");

  await assert.rejects(syncEmbeddedUI(path.join(root, "missing"), destination), /source is not a directory/);
  assert.equal(await readFile(path.join(destination, "current.js"), "utf8"), "must remain");
});

test("syncEmbeddedUI removes all generated output when the source is empty", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await writeFile(path.join(destination, "old.js"), "stale");
  await writeFile(path.join(destination, ".gitkeep"), "keep");

  await syncEmbeddedUI(source, destination);

  assert.deepEqual(await readdir(destination), [".gitkeep"]);
});

test("syncEmbeddedUI replaces a destination symlink instead of following it", async (t) => {
  const { root, source, destination } = await temporaryTree(t);
  const outside = path.join(root, "outside.js");
  await writeFile(outside, "outside");
  await writeFile(path.join(source, "index.js"), "embedded");
  await (await import("node:fs/promises")).symlink(outside, path.join(destination, "index.js"));

  await syncEmbeddedUI(source, destination);

  assert.equal(await readFile(outside, "utf8"), "outside");
  assert.equal(await readFile(path.join(destination, "index.js"), "utf8"), "embedded");
});

test("syncEmbeddedUI replaces a destination hard link without changing its peer", async (t) => {
  const { root, source, destination } = await temporaryTree(t);
  const outside = path.join(root, "outside.js");
  await writeFile(outside, "outside");
  await writeFile(path.join(source, "index.js"), "embedded");
  await link(outside, path.join(destination, "index.js"));

  await syncEmbeddedUI(source, destination);

  assert.equal(await readFile(outside, "utf8"), "outside");
  assert.equal(await readFile(path.join(destination, "index.js"), "utf8"), "embedded");
});

test("syncEmbeddedUI rejects a source .gitkeep symlink", async (t) => {
  const { root, source, destination } = await temporaryTree(t);
  const outside = path.join(root, "outside");
  await writeFile(outside, "outside");
  await writeFile(path.join(destination, "current.js"), "must remain");
  await (await import("node:fs/promises")).symlink(outside, path.join(source, ".gitkeep"));

  await assert.rejects(syncEmbeddedUI(source, destination), /unsupported entry/);
  assert.equal(await readFile(path.join(destination, "current.js"), "utf8"), "must remain");
});

test("publishEmbeddedUI rejects empty build output before changing the destination", async (t) => {
  const { source, destination } = await temporaryTree(t);
  await writeFile(path.join(destination, "current.js"), "must remain");

  await assert.rejects(publishEmbeddedUI(source, destination), /non-empty index.html/);
  assert.equal(await readFile(path.join(destination, "current.js"), "utf8"), "must remain");
});

test("ensureEmbedStub creates the exact placeholder without overwriting it", async (t) => {
  const { root } = await temporaryTree(t);
  const destination = path.join(root, "missing", "embed-dist");

  await ensureEmbedStub(destination);
  assert.equal(await readFile(path.join(destination, ".gitkeep"), "utf8"), "# Placeholder for //go:embed\n");

  await writeFile(path.join(destination, ".gitkeep"), "custom placeholder");
  await ensureEmbedStub(destination);
  assert.equal(await readFile(path.join(destination, ".gitkeep"), "utf8"), "custom placeholder");
});

test("ensureEmbedStub rejects a destination-root junction", async (t) => {
  const { root } = await temporaryTree(t);
  const outside = path.join(root, "outside");
  const destination = path.join(root, "embed-link");
  await mkdir(outside);
  await (await import("node:fs/promises")).symlink(outside, destination, "junction");

  await assert.rejects(ensureEmbedStub(destination), /destination is not a directory/);
  await assert.rejects(readFile(path.join(outside, ".gitkeep")), { code: "ENOENT" });
});

test("ensureWebDependencies accepts a healthy Windows dependency tree without reinstalling", async () => {
  const commands = [];
  const output = [];
  await ensureWebDependencies({
    webDirectory: "C:\\repo\\web",
    platform: "win32",
    inspect: async () => ({
      viteLaunchers: { windows: true, unix: false },
      installedLockExists: true,
      packageMtimeMs: 100,
      packageLockMtimeMs: 100,
      installedLockMtimeMs: 100,
    }),
    probeRolldown: async () => true,
    run: async (...args) => commands.push(args),
    write: (message) => output.push(message),
  });

  assert.deepEqual(commands, []);
  assert.deepEqual(output, []);
});

test("ensureWebDependencies repairs missing Vite with npm.cmd before verifying Rolldown", async () => {
  const events = [];
  await ensureWebDependencies({
    webDirectory: "C:\\repo\\web",
    platform: "win32",
    inspect: async () => ({
      viteLaunchers: { windows: false, unix: false },
      installedLockExists: false,
      packageMtimeMs: 300,
      packageLockMtimeMs: 300,
      installedLockMtimeMs: 100,
    }),
    probeRolldown: async (_directory, options) => {
      events.push(["probe", options]);
      return true;
    },
    run: async (...args) => events.push(["run", ...args]),
    write: (message) => events.push(["write", message]),
  });

  assert.deepEqual(events, [
    ["write", "web dependencies: Vite is not installed; running npm ci\n"],
    ["run", "npm.cmd", ["ci"], { cwd: "C:\\repo\\web" }],
    ["probe", { quiet: false }],
  ]);
});

test("webDependencyRepairReason preserves the dependency guard priority", () => {
  const healthy = {
    viteLaunchers: { windows: true, unix: true },
    installedLockExists: true,
    packageMtimeMs: 100,
    packageLockMtimeMs: 100,
    installedLockMtimeMs: 100,
    rolldownUsable: true,
  };

  assert.equal(webDependencyRepairReason({ ...healthy, viteLaunchers: { windows: false, unix: false }, installedLockExists: false }, "win32"), "Vite is not installed");
  assert.equal(webDependencyRepairReason({ ...healthy, installedLockExists: false }, "linux"), "the installed dependency lock is missing");
  assert.equal(webDependencyRepairReason({ ...healthy, packageMtimeMs: 101 }, "linux"), "the installed dependencies are stale");
  assert.equal(webDependencyRepairReason({ ...healthy, packageLockMtimeMs: 101 }, "linux"), "the installed dependencies are stale");
  assert.equal(webDependencyRepairReason({ ...healthy, rolldownUsable: false }, "linux"), "the platform-specific Rolldown binding is missing");
  assert.equal(webDependencyRepairReason(healthy, "linux"), null);
});

test("ensureWebDependencies repairs an unusable Rolldown binding", async () => {
  const events = [];
  await ensureWebDependencies({
    webDirectory: "/repo/web",
    platform: "linux",
    inspect: async () => ({
      viteLaunchers: { windows: false, unix: true },
      installedLockExists: true,
      packageMtimeMs: 100,
      packageLockMtimeMs: 100,
      installedLockMtimeMs: 100,
    }),
    probeRolldown: async (_directory, options) => {
      events.push(["probe", options]);
      return options.quiet === false;
    },
    run: async (...args) => events.push(["run", ...args]),
    write: (message) => events.push(["write", message]),
  });

  assert.deepEqual(events, [
    ["probe", { quiet: true }],
    ["write", "web dependencies: the platform-specific Rolldown binding is missing; running npm ci\n"],
    ["run", "npm", ["ci"], { cwd: "/repo/web" }],
    ["probe", { quiet: false }],
  ]);
});

test("ensureWebDependencies fails when Rolldown remains unavailable after repair", async () => {
  await assert.rejects(ensureWebDependencies({
    webDirectory: "/repo/web",
    platform: "linux",
    inspect: async () => ({
      viteLaunchers: { windows: false, unix: true },
      installedLockExists: true,
      packageMtimeMs: 101,
      packageLockMtimeMs: 100,
      installedLockMtimeMs: 100,
    }),
    probeRolldown: async () => false,
    run: async () => {},
    write: () => {},
  }), /Rolldown is unavailable after npm ci/);
});

test("ensureWebDependencies propagates npm failure without a final Rolldown probe", async () => {
  let probes = 0;
  await assert.rejects(ensureWebDependencies({
    webDirectory: "/repo/web",
    platform: "linux",
    inspect: async () => ({
      viteLaunchers: { windows: false, unix: true },
      installedLockExists: false,
      packageMtimeMs: 100,
      packageLockMtimeMs: 100,
      installedLockMtimeMs: 0,
    }),
    probeRolldown: async () => {
      probes += 1;
      return true;
    },
    run: async () => { throw new Error("npm ci failed"); },
    write: () => {},
  }), /npm ci failed/);
  assert.equal(probes, 0);
});

test("runWebBuildCommand dispatches build-ui in repository-relative paths", async () => {
  const events = [];
  await runWebBuildCommand("build-ui", {
    repoRoot: "C:\\repo",
    platform: "win32",
    ensureDependencies: async () => events.push(["ensure"]),
    run: async (...args) => events.push(["run", ...args]),
    sync: async (...args) => events.push(["sync", ...args]),
    ensureStub: async (...args) => events.push(["stub", ...args]),
  });
  assert.deepEqual(events, [
    ["ensure"],
    ["run", "npm.cmd", ["run", "build"], { cwd: path.win32.join("C:\\repo", "web") }],
    ["sync", path.win32.join("C:\\repo", "web", "dist"), path.win32.join("C:\\repo", "internal", "daemon", "webfs", "dist")],
    ["stub", path.win32.join("C:\\repo", "internal", "daemon", "webfs", "dist")],
  ]);
});

test("web build CLI reports an unknown command from any working directory", async (t) => {
  const { root } = await temporaryTree(t);
  const cli = path.join(import.meta.dirname, "web-build-cli.mjs");
  const result = await new Promise((resolve) => {
    const child = spawn(process.execPath, [cli, "unknown"], { cwd: root });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("exit", (code) => resolve({ code, stderr }));
  });
  assert.equal(result.code, 1);
  assert.match(result.stderr, /Unknown web build command: unknown/);
});
