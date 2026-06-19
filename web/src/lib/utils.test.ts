import { describe, it, expect } from "vitest";
import { cn } from "./utils";

// Smoke test: validates the Vitest harness, the @/ alias, and the cn() helper
// (which every primitive relies on) in one go.
describe("cn", () => {
  it("merges class strings", () => {
    expect(cn("a", "b")).toBe("a b");
  });

  it("respects falsy values and conditional logic", () => {
    expect(cn("a", false, undefined, null, "c")).toBe("a c");
  });

  it("lets later Tailwind classes override earlier ones", () => {
    // twMerge: p-4 should win over p-2.
    expect(cn("p-2", "p-4")).toBe("p-4");
  });
});
