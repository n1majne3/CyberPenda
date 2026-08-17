import path from "node:path";
import { fileURLToPath } from "node:url";

import { productionWebBuildOptions, runWebBuildCommand } from "./web-build.mjs";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

try {
  await runWebBuildCommand(process.argv[2] || "", productionWebBuildOptions(repoRoot));
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
}
