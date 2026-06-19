import { describe, it, expect } from "vitest";
import config from "./tailwind.config";

// The Tailwind config wires CSS tokens to utility classes. Assert the keys so
// primitives and pages can rely on `bg-brand`, `text-success`, `rounded-4xl`,
// the sidebar palette, and `ring-3` existing.
const colors = config.theme.extend.colors as Record<string, unknown>;
const colorKey = (name: string) =>
  typeof colors[name] === "object"
    ? (colors[name] as { DEFAULT: string }).DEFAULT
    : (colors[name] as string);

describe("tailwind config design tokens", () => {
  it("uses class-based dark mode", () => {
    expect(config.darkMode).toEqual(["class"]);
  });

  it.each([
    "brand",
    "success",
    "info",
    "warning",
    "primary",
    "destructive",
  ] as const)("exposes a %s color bound to a CSS token", (name) => {
    expect(colorKey(name)).toMatch(/hsl\(var\(--/);
  });

  it("exposes the sidebar palette", () => {
    expect(colors.sidebar).toBeDefined();
    expect((colors.sidebar as { DEFAULT: string }).DEFAULT).toMatch(/var\(--sidebar\)/);
  });

  it("exposes the multica radius scale (incl. pill 4xl for badges)", () => {
    const radii = config.theme.extend.borderRadius as Record<string, string>;
    for (const key of ["sm", "md", "lg", "xl", "2xl", "4xl"]) {
      expect(radii[key]).toBeDefined();
    }
    // 4xl is the pill radius used by badges.
    expect(radii["4xl"]).toMatch(/var\(--radius\)/);
  });

  it("extends ring widths so ring-3 is available (multica focus style)", () => {
    const ringWidth = config.theme.extend.ringWidth as Record<string, string> | undefined;
    expect(ringWidth?.["3"]).toBeDefined();
  });
});
