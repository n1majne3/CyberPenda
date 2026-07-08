import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { basename, dirname, extname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const srcDir = dirname(fileURLToPath(import.meta.url));
const thisFile = basename(fileURLToPath(import.meta.url));

const scannedExtensions = new Set([".css", ".ts", ".tsx"]);

const staleContracts = [
  { name: "Multica product name", pattern: /\bmultica\b/i },
  { name: "legacy brand token", pattern: /--brand\b/ },
  { name: "legacy Inter font token", pattern: /--font-inter\b/ },
  { name: "oversized card radius", pattern: /\brounded-(?:xl|2xl|3xl|4xl)\b/ },
  { name: "oversized focus ring", pattern: /\bfocus-visible:ring-3\b/ },
  { name: "press-scale interaction", pattern: /\bactive:scale-\[0\.98\]\b/ },
  { name: "dark overlay input background", pattern: /\bdark:bg-input\/30\b/ },
  { name: "raw blue active filter", pattern: /\bbg-blue-500\/10\b/ },
] as const;

function collectSourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    const stat = statSync(path);
    if (stat.isDirectory()) {
      return collectSourceFiles(path);
    }
    if (entry === thisFile || !scannedExtensions.has(extname(entry))) {
      return [];
    }
    return [path];
  });
}

describe("Geist regression source checks", () => {
  it("keeps obsolete Multica styling contracts out of frontend source and tests", () => {
    const matches = collectSourceFiles(srcDir).flatMap((path) => {
      const source = readFileSync(path, "utf8");
      return staleContracts
        .filter(({ pattern }) => pattern.test(source))
        .map(({ name }) => `${relative(srcDir, path)}: ${name}`);
    });

    expect(matches).toEqual([]);
  });
});
