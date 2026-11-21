import { describe, expect, it } from "vitest";
import { formatClockTime, formatCompactDateTime, formatDateTime } from "./format";

describe("date formatting", () => {
  it("returns an empty string for an invalid timestamp instead of throwing", () => {
    expect(formatDateTime("not a date")).toBe("");
    expect(formatCompactDateTime("")).toBe("");
    expect(formatClockTime(Number.NaN)).toBe("");
  });

  it("formats a valid timestamp", () => {
    expect(formatDateTime("2026-01-01T00:00:00Z")).not.toBe("");
  });
});
