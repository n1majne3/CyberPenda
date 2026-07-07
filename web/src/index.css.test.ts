import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// The CSS theme is the foundation every primitive and page relies on. Rather
// than render a component, we assert directly on the token file so the test
// is fast, deterministic, and documents which variables the design system
// guarantees.
const cssPath = resolve(dirname(fileURLToPath(import.meta.url)), "index.css");
const css = readFileSync(cssPath, "utf8");

// Color/surface tokens that must be defined separately per theme (light vs dark).
const THEME_TOKENS = [
  "--background",
  "--foreground",
  "--card",
  "--card-foreground",
  "--primary",
  "--primary-foreground",
  "--secondary",
  "--secondary-foreground",
  "--muted",
  "--muted-foreground",
  "--accent",
  "--accent-foreground",
  "--destructive",
  "--destructive-foreground",
  "--border",
  "--input",
  "--ring",
  // Multica additions
  "--brand",
  "--brand-foreground",
  "--success",
  "--warning",
  "--warning-foreground",
  "--sidebar",
  "--sidebar-foreground",
  "--sidebar-accent",
  "--sidebar-border",
] as const;

// Tokens that are theme-independent (defined once on :root).
const GLOBAL_TOKENS = ["--radius", "--font-inter", "--font-mono"] as const;

function tokenBlock(scope: string): string {
  // Grab the body of the `:root { ... }` or `.dark { ... }` rule.
  const re = new RegExp(`${scope.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([\\s\\S]*?)\\}`);
  const m = css.match(re);
  return m ? m[1] : "";
}

describe("index.css design tokens", () => {
  it("defines a light :root theme", () => {
    expect(css).toMatch(/:root\s*\{/);
  });

  it("defines a .dark theme", () => {
    expect(css).toMatch(/\.dark\s*\{/);
  });

  it.each(THEME_TOKENS)("light :root defines %s", (token) => {
    expect(tokenBlock(":root")).toContain(token);
  });

  it.each(THEME_TOKENS)(".dark defines %s", (token) => {
    expect(tokenBlock(".dark")).toContain(token);
  });

  it.each(GLOBAL_TOKENS)(":root defines global %s", (token) => {
    expect(tokenBlock(":root")).toContain(token);
  });

  it("uses the blue brand hue for --primary (multica hue 255)", () => {
    // Blue sits roughly in hue 210–250 in HSL. Assert --primary is blue-ish.
    const light = tokenBlock(":root");
    expect(light).toMatch(/--primary:\s*2[0-4]\d\s/);
  });

  it("sets global focus, touch, and overflow interaction defaults", () => {
    expect(css).toContain(":focus-visible");
    expect(css).toContain("touch-action: manipulation");
    expect(css).toContain("-webkit-tap-highlight-color");
    expect(css).toContain("overflow-x: hidden");
  });
});
