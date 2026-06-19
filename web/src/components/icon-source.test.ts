import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const srcRoot = join(process.cwd(), "src");

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    if (entry === "test" || entry.endsWith(".test.ts") || entry.endsWith(".test.tsx")) return [];
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx)$/.test(entry) ? [path] : [];
  });
}

describe("icon source policy", () => {
  it("uses lucide icons instead of inline svg/emoji/image icons", () => {
    const violations = sourceFiles(srcRoot).flatMap((file) => {
      const rel = relative(srcRoot, file);
      if (rel === "components/Logo.tsx") return [];

      const source = readFileSync(file, "utf8");
      const found = [];
      if (/<svg\b/.test(source)) found.push("inline svg");
      if (/<img\b/.test(source)) found.push("image icon");
      if (/[🧪📋🔎✅❌⚠️🚀🔐🛡️📁📄]/u.test(source)) found.push("emoji icon");
      return found.map((kind) => `${rel}: ${kind}`);
    });

    expect(violations).toEqual([]);
  });
});
