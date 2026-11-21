import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const srcRoot = join(process.cwd(), "src");

const emojiPattern = new RegExp(
  "[" + String.fromCodePoint(
    0x1f9ea,
    0x1f4cb,
    0x1f50e,
    0x2705,
    0x274c,
    0x26a0,
    0x1f680,
    0x1f510,
    0x1f6e1,
    0x1f4c1,
    0x1f4c4,
  ) + "]",
  "u",
);

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
      const rel = relative(srcRoot, file).replaceAll("\\", "/");
      if (rel === "components/Logo.tsx") return [];

      const source = readFileSync(file, "utf8");
      const found = [];
      const graphSource = rel === "pages/FGSPage.tsx";
      if (/<svg\b/.test(source) && !graphSource) found.push("inline svg");
      if (/<img\b/.test(source)) found.push("image icon");
      if (emojiPattern.test(source)) found.push("emoji icon");
      return found.map((kind) => `${rel}: ${kind}`);
    });

    expect(violations).toEqual([]);
  });
});
