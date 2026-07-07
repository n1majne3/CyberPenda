import { readFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const htmlPath = resolve(dirname(fileURLToPath(import.meta.url)), "../index.html");
const html = readFileSync(htmlPath, "utf8");

describe("index.html", () => {
  it("declares theme colors for browser chrome", () => {
    expect(html).toContain('name="theme-color"');
    expect(html).toContain('media="(prefers-color-scheme: light)"');
    expect(html).toContain('media="(prefers-color-scheme: dark)"');
  });
});
