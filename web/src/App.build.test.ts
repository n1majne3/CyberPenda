import { describe, expect, it } from "vitest";
import { build, type Rollup } from "vite";

describe("application build", () => {
  it("emits route pages as lazy chunks", async () => {
    const result = await build({
      root: process.cwd(),
      logLevel: "silent",
      build: { write: false },
    });
    const outputs = Array.isArray(result) ? result : [result];
    const chunks = outputs.flatMap((output) =>
      output.output.filter((item): item is Rollup.OutputChunk => item.type === "chunk"),
    );
    const lazyPageChunks = chunks.filter(
      (chunk) => chunk.isDynamicEntry && /Page-[A-Za-z0-9_-]+\.js$/.test(chunk.fileName),
    );

    expect(lazyPageChunks.length).toBeGreaterThanOrEqual(10);
  }, 30_000);
});
