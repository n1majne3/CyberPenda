import { describe, it, expect } from "vitest";
import config from "./tailwind.config";

// The Tailwind config wires CSS tokens to utility classes. Assert the keys so
// primitives and pages can rely on neutral surfaces, state colors, tight radii,
// and Geist font variables.
const colors = config.theme.extend.colors as Record<string, unknown>;
const colorKey = (name: string) =>
  typeof colors[name] === "object"
    ? (colors[name] as { DEFAULT: string }).DEFAULT
    : (colors[name] as string);

describe("tailwind config design tokens", () => {
  it("uses class-based dark mode", () => {
    expect(config.darkMode).toEqual(["class"]);
  });

  it.each(["success", "info", "warning", "primary", "destructive"] as const)(
    "exposes a %s color bound to a CSS token",
    (name) => {
    expect(colorKey(name)).toMatch(/hsl\(var\(--/);
    },
  );

  it("does not expose the removed brand accent token", () => {
    expect(colors.brand).toBeUndefined();
  });

  it("exposes the sidebar palette", () => {
    expect(colors.sidebar).toBeDefined();
    expect((colors.sidebar as { DEFAULT: string }).DEFAULT).toMatch(/var\(--sidebar\)/);
  });

  it("exposes a tight Geist radius scale", () => {
    const radii = config.theme.extend.borderRadius as Record<string, string>;
    for (const key of ["sm", "md", "lg", "xl"]) {
      expect(radii[key]).toBeDefined();
    }
    expect(radii.sm).toBe("0.125rem");
    expect(radii.md).toBe("0.25rem");
    expect(radii.lg).toBe("var(--radius)");
    expect(radii.xl).toBe("0.5rem");
    expect(radii["4xl"]).toBeUndefined();
  });

  it("uses Tailwind's default ring-2 focus width", () => {
    const ringWidth = config.theme.extend.ringWidth as Record<string, string> | undefined;
    expect(ringWidth).toBeUndefined();
  });

  it("maps sans and mono fonts to Geist variables", () => {
    const fontFamily = config.theme.extend.fontFamily as Record<string, string[]>;
    expect(fontFamily.sans[0]).toBe("var(--font-geist-sans)");
    expect(fontFamily.mono[0]).toBe("var(--font-geist-mono)");
  });
});
