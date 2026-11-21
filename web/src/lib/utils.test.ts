import { describe, it, expect } from "vitest";
import { cn } from "./utils";

describe("cn", () => {
  it("merges class strings", () => {
    expect(cn("a", "b")).toBe("a b");
  });

  it("respects falsy values and conditional logic", () => {
    expect(cn("a", false, undefined, null, "c")).toBe("a c");
  });

  it("lets later Tailwind classes override earlier ones", () => {
    expect(cn("p-2", "p-4")).toBe("p-4");
  });
});
