import { afterEach, expect, it, vi } from "vitest";
import { downloadMarkdown } from "./download";

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

it("keeps the object URL until the download click has run", () => {
  vi.useFakeTimers();
  const createObjectURL = vi.fn(() => "blob:report");
  const revokeObjectURL = vi.fn();
  Object.assign(URL, { createObjectURL, revokeObjectURL });
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

  downloadMarkdown("# Report", "report.md");

  expect(click).toHaveBeenCalledTimes(1);
  expect(revokeObjectURL).not.toHaveBeenCalled();
  vi.runAllTimers();
  expect(revokeObjectURL).toHaveBeenCalledWith("blob:report");
});
