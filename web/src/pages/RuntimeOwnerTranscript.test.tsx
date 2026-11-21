import { createRef } from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TranscriptList } from "./RuntimeOwnerTranscript";
import { buildTranscriptRows } from "./runtimeOwnerView";

describe("TranscriptList", () => {
  it("renders error transcript entries visibly as alerts", () => {
    const rows = buildTranscriptRows([
      {
        id: "ev-1-error",
        seq: 1,
        continuation: 1,
        kind: "error",
        role: "runtime",
        text: "400 request (13032 tokens) exceeds the available context size (8192 tokens)",
        status: "failed",
        created_at: "2026-11-21T14:00:00Z",
      },
    ]);

    render(
      <TranscriptList
        rows={rows}
        rowsRef={() => {}}
        endRef={createRef<HTMLDivElement>()}
        runtimeLabel="Pi"
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert).toBeTruthy();
    expect(alert.textContent).toContain("exceeds the available context size");
  });
});
