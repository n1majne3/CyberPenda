import { describe, expect, it } from "vitest";
import { demoApiGet } from "./demoApi";

describe("demo asset inventory", () => {
  it("serves assets and coverage for the demo Project", () => {
    const assets = demoApiGet("/api/projects/demo-project/assets") as { assets: unknown[] };
    const coverage = demoApiGet("/api/projects/demo-project/assets/coverage") as { coverage_percent: number };
    expect(assets.assets.length).toBeGreaterThan(0);
    expect(coverage.coverage_percent).toBeGreaterThan(0);
  });
});
