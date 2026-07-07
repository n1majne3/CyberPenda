import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const srcRoot = join(process.cwd(), "src");

// Build the emoji character class from code points. Composing it from literals
// (e.g. /[\u2705\u274C...]/) trips eslint's no-misleading-character-class rule
// because some emoji carry combining marks; String.fromCodePoint avoids that.
const emojiPattern = new RegExp(
  "[" + String.fromCodePoint(
    0x1f9ea, // 🧪 test tube
    0x1f4cb, // 📋 clipboard
    0x1f50e, // 🔎 magnifying glass
    0x2705,  // ✅ check mark button
    0x274c,  // ❌ cross mark
    0x26a0,  // ⚠ warning sign
    0x1f680, // 🚀 rocket
    0x1f510, // 🔐 lock with key
    0x1f6e1, // 🛡️ shield
    0x1f4c1, // 📁 folder
    0x1f4c4, // 📄 page facing up
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
      const rel = relative(srcRoot, file);
      if (rel === "components/Logo.tsx") return [];

      const source = readFileSync(file, "utf8");
      const found = [];
      if (/<svg\b/.test(source)) found.push("inline svg");
      if (/<img\b/.test(source)) found.push("image icon");
      if (emojiPattern.test(source)) found.push("emoji icon");
      return found.map((kind) => `${rel}: ${kind}`);
    });

    expect(violations).toEqual([]);
  });
});
