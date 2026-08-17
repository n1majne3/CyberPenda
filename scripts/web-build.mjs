import { spawn } from "node:child_process";
import { access, constants, copyFile, lstat, mkdir, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";

const PROTECTED_NAME = ".gitkeep";

async function pathStat(target) {
  try {
    return await lstat(target);
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

async function validateSourceDirectory(directory) {
  const info = await pathStat(directory);
  if (!info?.isDirectory()) {
    throw new Error(`UI build source is not a directory: ${directory}`);
  }

  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const entryPath = path.join(directory, entry.name);
    if (entry.name === PROTECTED_NAME) {
      if (!entry.isFile()) {
        throw new Error(`UI build source contains an unsupported entry: ${entryPath}`);
      }
      continue;
    }
    if (entry.isDirectory()) {
      await validateSourceDirectory(entryPath);
    } else if (!entry.isFile()) {
      throw new Error(`UI build source contains an unsupported entry: ${entryPath}`);
    }
  }
}

async function removeGeneratedEntry(target) {
  const info = await pathStat(target);
  if (!info) return;
  if (!info.isDirectory()) {
    await rm(target, { force: true });
    return;
  }

  for (const entry of await readdir(target, { withFileTypes: true })) {
    if (entry.name === PROTECTED_NAME) continue;
    await removeGeneratedEntry(path.join(target, entry.name));
  }
  if ((await readdir(target)).length === 0) {
    await rm(target, { recursive: true });
  }
}

async function syncDirectory(source, destination) {
  const sourceEntries = new Map(
    (await readdir(source, { withFileTypes: true }))
      .filter((entry) => entry.name !== PROTECTED_NAME)
      .map((entry) => [entry.name, entry]),
  );

  for (const destinationEntry of await readdir(destination, { withFileTypes: true })) {
    if (destinationEntry.name === PROTECTED_NAME) continue;
    if (!sourceEntries.has(destinationEntry.name)) {
      await removeGeneratedEntry(path.join(destination, destinationEntry.name));
    }
  }

  for (const [name, sourceEntry] of sourceEntries) {
    const sourcePath = path.join(source, name);
    const destinationPath = path.join(destination, name);
    const destinationInfo = await pathStat(destinationPath);

    if (sourceEntry.isDirectory()) {
      if (destinationInfo && !destinationInfo.isDirectory()) {
        await rm(destinationPath, { recursive: true, force: true });
      }
      await mkdir(destinationPath, { recursive: true });
      await syncDirectory(sourcePath, destinationPath);
      continue;
    }

    if (destinationInfo?.isDirectory()) {
      await removeGeneratedEntry(destinationPath);
      const remaining = await pathStat(destinationPath);
      if (remaining) {
        throw new Error(`Cannot replace protected directory with UI file: ${destinationPath}`);
      }
    } else if (destinationInfo) {
      await rm(destinationPath, { force: true });
    }
    await copyFile(sourcePath, destinationPath);
  }
}

export async function syncEmbeddedUI(source, destination) {
  await validateSourceDirectory(source);
  const destinationInfo = await pathStat(destination);
  if (destinationInfo && !destinationInfo.isDirectory()) {
    throw new Error(`UI embed destination is not a directory: ${destination}`);
  }
  await mkdir(destination, { recursive: true });
  await syncDirectory(source, destination);
}

export async function publishEmbeddedUI(source, destination) {
  const indexInfo = await pathStat(path.join(source, "index.html"));
  if (!indexInfo?.isFile() || indexInfo.size === 0) {
    throw new Error(`UI build source must contain a non-empty index.html: ${source}`);
  }
  await syncEmbeddedUI(source, destination);
}

export async function ensureEmbedStub(destination) {
  const destinationInfo = await pathStat(destination);
  if (destinationInfo && !destinationInfo.isDirectory()) {
    throw new Error(`UI embed destination is not a directory: ${destination}`);
  }
  await mkdir(destination, { recursive: true });
  const placeholder = path.join(destination, PROTECTED_NAME);
  if (!(await pathStat(placeholder))) {
    await writeFile(placeholder, "# Placeholder for //go:embed\n");
  }
}

export function webDependencyRepairReason(state, platform) {
  const viteInstalled = platform === "win32"
    ? state.viteLaunchers.windows
    : state.viteLaunchers.unix;
  if (!viteInstalled) return "Vite is not installed";
  if (!state.installedLockExists) return "the installed dependency lock is missing";
  if (state.packageMtimeMs > state.installedLockMtimeMs || state.packageLockMtimeMs > state.installedLockMtimeMs) {
    return "the installed dependencies are stale";
  }
  if (!state.rolldownUsable) return "the platform-specific Rolldown binding is missing";
  return null;
}

export async function ensureWebDependencies({ inspect, platform, probeRolldown, run, webDirectory, write }) {
  const state = await inspect(webDirectory);
  let reason = webDependencyRepairReason({ ...state, rolldownUsable: true }, platform);
  if (!reason) {
    const rolldownUsable = await probeRolldown(webDirectory, { quiet: true });
    reason = webDependencyRepairReason({ ...state, rolldownUsable }, platform);
  }
  if (!reason) return;
  write(`web dependencies: ${reason}; running npm ci\n`);
  await run(platform === "win32" ? "npm.cmd" : "npm", ["ci"], { cwd: webDirectory });
  const verified = await probeRolldown(webDirectory, { quiet: false });
  if (!verified) throw new Error("Rolldown is unavailable after npm ci");
}

export async function runWebBuildCommand(command, {
  ensureDependencies,
  ensureStub,
  platform,
  repoRoot,
  run,
  sync,
}) {
  const pathAPI = platform === "win32" ? path.win32 : path;
  const webDirectory = pathAPI.join(repoRoot, "web");
  const embedDirectory = pathAPI.join(repoRoot, "internal", "daemon", "webfs", "dist");

  if (command === "ensure-deps") {
    await ensureDependencies();
    return;
  }
  if (command === "ensure-embed-stub") {
    await ensureStub(embedDirectory);
    return;
  }
  if (command !== "build-ui") {
    throw new Error(`Unknown web build command: ${command}`);
  }

  await ensureDependencies();
  await run(platform === "win32" ? "npm.cmd" : "npm", ["run", "build"], { cwd: webDirectory });
  await sync(pathAPI.join(webDirectory, "dist"), embedDirectory);
  await ensureStub(embedDirectory);
}

async function exists(target, mode) {
  try {
    await access(target, mode);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT" || error?.code === "EACCES") return false;
    throw error;
  }
}

export async function inspectWebDependencies(webDirectory) {
  const installedLock = path.join(webDirectory, "node_modules", ".package-lock.json");
  const [packageInfo, packageLockInfo, installedLockInfo] = await Promise.all([
    stat(path.join(webDirectory, "package.json")),
    stat(path.join(webDirectory, "package-lock.json")),
    pathStat(installedLock),
  ]);
  return {
    viteLaunchers: {
      windows: await exists(path.join(webDirectory, "node_modules", ".bin", "vite.cmd"), constants.F_OK),
      unix: await exists(path.join(webDirectory, "node_modules", ".bin", "vite"), constants.X_OK),
    },
    installedLockExists: installedLockInfo?.isFile() === true,
    packageMtimeMs: packageInfo.mtimeMs,
    packageLockMtimeMs: packageLockInfo.mtimeMs,
    installedLockMtimeMs: installedLockInfo?.mtimeMs ?? 0,
  };
}

export async function runProcess(command, args, { cwd, quiet = false } = {}) {
  const windowsCommand = process.platform === "win32" && command.toLowerCase().endsWith(".cmd");
  const executable = windowsCommand ? (process.env.ComSpec || "cmd.exe") : command;
  const executableArgs = windowsCommand ? ["/d", "/s", "/c", command, ...args] : args;
  await new Promise((resolve, reject) => {
    const child = spawn(executable, executableArgs, {
      cwd,
      stdio: quiet ? "ignore" : "inherit",
      windowsVerbatimArguments: false,
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`${command} exited with ${signal ? `signal ${signal}` : `code ${code}`}`));
    });
  });
}

export async function probeRolldown(webDirectory, { quiet }) {
  try {
    await runProcess(process.execPath, ["-e", "import('rolldown')"], { cwd: webDirectory, quiet });
    return true;
  } catch (error) {
    if (quiet) return false;
    throw error;
  }
}

export function productionWebBuildOptions(repoRoot) {
  const webDirectory = path.join(repoRoot, "web");
  const ensureDependencies = () => ensureWebDependencies({
    webDirectory,
    platform: process.platform,
    inspect: inspectWebDependencies,
    probeRolldown,
    run: runProcess,
    write: (message) => process.stdout.write(message),
  });
  return {
    repoRoot,
    platform: process.platform,
    ensureDependencies,
    ensureStub: ensureEmbedStub,
    run: runProcess,
    sync: publishEmbeddedUI,
  };
}
